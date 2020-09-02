package controller

import (
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/controller"

	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/types"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	lhinformers "github.com/longhorn/longhorn-manager/k8s/pkg/client/informers/externalversions/longhorn/v1beta1"
)

type KubernetesNodeController struct {
	*baseController

	controllerID string

	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	ds *datastore.DataStore

	nStoreSynced  cache.InformerSynced
	sStoreSynced  cache.InformerSynced
	knStoreSynced cache.InformerSynced
}

func NewKubernetesNodeController(
	logger logrus.FieldLogger,
	ds *datastore.DataStore,
	scheme *runtime.Scheme,
	nodeInformer lhinformers.NodeInformer,
	settingInformer lhinformers.SettingInformer,
	kubeNodeInformer coreinformers.NodeInformer,
	kubeClient clientset.Interface,
	controllerID string) *KubernetesNodeController {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logrus.Infof)
	// TODO: remove the wrapper when every clients have moved to use the clientset.
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: v1core.New(kubeClient.CoreV1().RESTClient()).Events("")})

	knc := &KubernetesNodeController{
		baseController: newBaseController("longhorn-kubernetes-node", logger),

		controllerID: controllerID,

		kubeClient:    kubeClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme, v1.EventSource{Component: "longhorn-kubernetes-node-controller"}),

		ds: ds,

		nStoreSynced:  nodeInformer.Informer().HasSynced,
		sStoreSynced:  settingInformer.Informer().HasSynced,
		knStoreSynced: kubeNodeInformer.Informer().HasSynced,
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    knc.enqueueLonghornNode,
		UpdateFunc: func(old, cur interface{}) { knc.enqueueLonghornNode(cur) },
		DeleteFunc: knc.enqueueLonghornNode,
	})

	settingInformer.Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: isSettingCreateDefaultDiskLabeledNodes,
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc:    knc.enqueueSetting,
				UpdateFunc: func(old, cur interface{}) { knc.enqueueSetting(cur) },
			},
		},
	)

	kubeNodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, cur interface{}) { knc.enqueueKubernetesNode(cur) },
		DeleteFunc: knc.enqueueKubernetesNode,
	})

	return knc
}

func isSettingCreateDefaultDiskLabeledNodes(obj interface{}) bool {
	setting, ok := obj.(*longhorn.Setting)
	if !ok {
		deletedState, ok := obj.(*cache.DeletedFinalStateUnknown)
		if !ok {
			return false
		}

		// use the last known state, to enqueue, dependent objects
		setting, ok = deletedState.Obj.(*longhorn.Setting)
		if !ok {
			return false
		}
	}

	return types.SettingName(setting.Name) == types.SettingNameCreateDefaultDiskLabeledNodes
}

func (knc *KubernetesNodeController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer knc.queue.ShutDown()

	logrus.Infof("Start Longhorn Kubernetes node controller")
	defer logrus.Infof("Shutting down Longhorn Kubernetes node controller")

	if !controller.WaitForCacheSync("longhorn kubernetes node", stopCh,
		knc.nStoreSynced, knc.sStoreSynced, knc.knStoreSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(knc.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (knc *KubernetesNodeController) worker() {
	for knc.processNextWorkItem() {
	}
}

func (knc *KubernetesNodeController) processNextWorkItem() bool {
	key, quit := knc.queue.Get()

	if quit {
		return false
	}
	defer knc.queue.Done(key)

	err := knc.syncKubernetesNode(key.(string))
	knc.handleErr(err, key)

	return true
}

func (knc *KubernetesNodeController) handleErr(err error, key interface{}) {
	if err == nil {
		knc.queue.Forget(key)
		return
	}

	if knc.queue.NumRequeues(key) < maxRetries {
		logrus.Warnf("Error syncing Longhorn node %v: %v", key, err)
		knc.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	logrus.Warnf("Dropping Longhorn node %v out of the queue: %v", key, err)
	knc.queue.Forget(key)
}

func (knc *KubernetesNodeController) syncKubernetesNode(key string) (err error) {
	defer func() {
		err = errors.Wrapf(err, "fail to sync node for %v", key)
	}()
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	kubeNode, err := knc.ds.GetKubernetesNode(name)
	if err != nil {
		if !datastore.ErrorIsNotFound(err) {
			return err
		}
		logrus.Errorf("Kubernetes node %v has been deleted", key)
	}

	if kubeNode == nil {
		if err := knc.ds.DeleteNode(name); err != nil {
			return err
		}
		return nil
	}

	defer func() {
		// requeue if it's conflict
		if apierrors.IsConflict(errors.Cause(err)) {
			logrus.Debugf("Requeue %v due to conflict: %v", key, err)
			knc.enqueueKubernetesNode(kubeNode)
			err = nil
		}
	}()

	if knc.controllerID != kubeNode.Name {
		// not our's
		return nil
	}

	node, err := knc.ds.GetNode(kubeNode.Name)
	if err != nil {
		// cannot find the Longhorn node, may be hasn't been created yet, don't need to to sync
		return nil
	}

	// sync default disks on labeled Nodes
	if err := knc.syncDefaultDisks(node); err != nil {
		return err
	}

	// sync node tags
	if err := knc.syncDefaultNodeTags(node); err != nil {
		return err
	}

	return nil
}

func (knc *KubernetesNodeController) enqueueSetting(obj interface{}) {
	node, err := knc.ds.GetKubernetesNode(knc.controllerID)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get kubernetes node %v: %v ", knc.controllerID, err))
		return
	}
	knc.enqueueKubernetesNode(node)
}

func (knc *KubernetesNodeController) enqueueLonghornNode(obj interface{}) {
	lhNode, ok := obj.(*longhorn.Node)
	if !ok {
		deletedState, ok := obj.(*cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", obj))
			return
		}

		// use the last known state, to enqueue, dependent objects
		lhNode, ok = deletedState.Obj.(*longhorn.Node)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("DeletedFinalStateUnknown contained invalid object: %#v", deletedState.Obj))
			return
		}
	}

	if lhNode.Name != knc.controllerID {
		return
	}
	node, err := knc.ds.GetKubernetesNode(lhNode.Name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get kubernetes node %v: %v ", knc.controllerID, err))
		return
	}
	knc.enqueueKubernetesNode(node)
}

func (knc *KubernetesNodeController) enqueueKubernetesNode(node interface{}) {
	key, err := controller.KeyFunc(node)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", node, err))
		return
	}

	knc.queue.AddRateLimited(key)
}

// syncDefaultDisks handles creation of the customized default Disk if the setting create-default-disk-labeled-nodes is enabled.
// This allows for the default Disk to be customized and created even if the node has been labeled after initial registration with Longhorn,
// provided that there are no existing disks remaining on the node.
func (knc *KubernetesNodeController) syncDefaultDisks(node *longhorn.Node) (err error) {
	requireLabel, err := knc.ds.GetSettingAsBool(types.SettingNameCreateDefaultDiskLabeledNodes)
	if err != nil {
		return err
	}
	if !requireLabel {
		return nil
	}

	// only apply default disk config if the node disk map is empty
	if len(node.Spec.DiskPathMap) != 0 {
		return nil
	}

	kubeNode, err := knc.ds.GetKubernetesNode(node.Name)
	if err != nil {
		return err
	}
	val, ok := kubeNode.Labels[types.NodeCreateDefaultDiskLabelKey]
	if !ok {
		return nil
	}
	val = strings.ToLower(val)

	switch val {
	case types.NodeCreateDefaultDiskLabelValueTrue:
		dataPath, err := knc.ds.GetSettingValueExisted(types.SettingNameDefaultDataPath)
		if err != nil {
			return err
		}
		if err := knc.ds.PrepareForDefaultDiskCreation(dataPath, node); err != nil {
			return err
		}
		if node, err = knc.ds.UpdateNode(node); err != nil {
			return errors.Wrapf(err, "Kubernetes node: Failed to update node %v during the default disk auto creation", node.Name)
		}
		go func() {
			if _, err := knc.ds.WaitForDiskCreation(node.Name, dataPath); err != nil {
				logrus.Warnf("Kubernetes node: Failed to wait for the default disk creation complete: %v", err)
			}
		}()
	case types.NodeCreateDefaultDiskLabelValueConfig:
		annotation, ok := kubeNode.Annotations[types.KubeNodeDefaultDiskConfigAnnotationKey]
		if !ok {
			return nil
		}
		go func() {
			if err = knc.ds.CreateDisksFromAnnotation(node.Name, annotation); err != nil {
				logrus.Warnf("Kubernetes node: Failed to create disks from annotation %v: %v: %v", types.KubeNodeDefaultDiskConfigAnnotationKey, val, err)
			}
		}()
	default:
		logrus.Warnf("Kubernetes node: invalid label value: %v: %v", types.NodeCreateDefaultDiskLabelKey, val)
		return nil
	}

	return nil
}

func (knc *KubernetesNodeController) syncDefaultNodeTags(node *longhorn.Node) error {
	if len(node.Spec.Tags) != 0 {
		return nil
	}

	kubeNode, err := knc.ds.GetKubernetesNode(node.Name)
	if err != nil {
		return err
	}

	if val, exist := kubeNode.Annotations[types.KubeNodeDefaultNodeTagConfigAnnotationKey]; exist {
		tags, err := types.GetNodeTagsFromAnnotation(val)
		if err != nil {
			logrus.Errorf("failed to set default node tags for node %v: %v", node.Name, err)
			return nil
		}
		node.Spec.Tags = tags

		updatedNode, err := knc.ds.UpdateNode(node)
		if err != nil {
			return err
		}
		node = updatedNode
	}
	return nil
}
