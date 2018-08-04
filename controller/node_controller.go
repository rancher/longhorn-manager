package controller

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"

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
	"k8s.io/client-go/util/workqueue"
	"k8s.io/kubernetes/pkg/controller"

	"github.com/rancher/longhorn-manager/datastore"
	"github.com/rancher/longhorn-manager/types"
	"github.com/rancher/longhorn-manager/util"

	longhorn "github.com/rancher/longhorn-manager/k8s/pkg/apis/longhorn/v1alpha1"
	lhinformers "github.com/rancher/longhorn-manager/k8s/pkg/client/informers/externalversions/longhorn/v1alpha1"
)

var (
	ownerKindNode = longhorn.SchemeGroupVersion.WithKind("Node").String()
)

type NodeController struct {
	// which namespace controller is running with
	namespace    string
	controllerID string

	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	ds *datastore.DataStore

	nStoreSynced cache.InformerSynced
	pStoreSynced cache.InformerSynced
	sStoreSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface
}

func NewNodeController(
	ds *datastore.DataStore,
	scheme *runtime.Scheme,
	nodeInformer lhinformers.NodeInformer,
	settingInformer lhinformers.SettingInformer,
	podInformer coreinformers.PodInformer,
	kubeClient clientset.Interface,
	namespace, controllerID string) *NodeController {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logrus.Infof)
	// TODO: remove the wrapper when every clients have moved to use the clientset.
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: v1core.New(kubeClient.CoreV1().RESTClient()).Events("")})

	nc := &NodeController{
		namespace:    namespace,
		controllerID: controllerID,

		kubeClient:    kubeClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme, v1.EventSource{Component: "longhorn-node-controller"}),

		ds: ds,

		nStoreSynced: nodeInformer.Informer().HasSynced,
		pStoreSynced: podInformer.Informer().HasSynced,
		sStoreSynced: settingInformer.Informer().HasSynced,

		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "longhorn-node"),
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			n := obj.(*longhorn.Node)
			nc.enqueueNode(n)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			cur := newObj.(*longhorn.Node)
			nc.enqueueNode(cur)
		},
		DeleteFunc: func(obj interface{}) {
			n := obj.(*longhorn.Node)
			nc.enqueueNode(n)
		},
	})

	settingInformer.Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				switch t := obj.(type) {
				case *longhorn.Setting:
					return filterSettings(t)
				default:
					utilruntime.HandleError(fmt.Errorf("unable to handle object in %T: %T", nc, obj))
					return false
				}
			},
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					s := obj.(*longhorn.Setting)
					nc.enqueueSetting(s)
				},
				UpdateFunc: func(oldObj, newObj interface{}) {
					cur := newObj.(*longhorn.Setting)
					nc.enqueueSetting(cur)
				},
			},
		},
	)

	return nc
}

func filterSettings(s *longhorn.Setting) bool {
	// filter that only StorageMinimalAvailablePercentage will impact disk status
	if types.SettingName(s.Name) == types.SettingNameStorageMinimalAvailablePercentage {
		return true
	}
	return false
}

func (nc *NodeController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer nc.queue.ShutDown()

	logrus.Infof("Start Longhorn node controller")
	defer logrus.Infof("Shutting down Longhorn node controller")

	if !controller.WaitForCacheSync("longhorn node", stopCh, nc.pStoreSynced, nc.nStoreSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(nc.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (nc *NodeController) worker() {
	for nc.processNextWorkItem() {
	}
}

func (nc *NodeController) processNextWorkItem() bool {
	key, quit := nc.queue.Get()

	if quit {
		return false
	}
	defer nc.queue.Done(key)

	err := nc.syncNode(key.(string))
	nc.handleErr(err, key)

	return true
}

func (nc *NodeController) handleErr(err error, key interface{}) {
	if err == nil {
		nc.queue.Forget(key)
		return
	}

	if nc.queue.NumRequeues(key) < maxRetries {
		logrus.Warnf("Error syncing Longhorn node %v: %v", key, err)
		nc.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	logrus.Warnf("Dropping Longhorn node %v out of the queue: %v", key, err)
	nc.queue.Forget(key)
}

func (nc *NodeController) syncNode(key string) (err error) {
	defer func() {
		err = errors.Wrapf(err, "fail to sync node for %v", key)
	}()
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	if namespace != nc.namespace {
		// Not ours, don't do anything
		return nil
	}

	node, err := nc.ds.GetNode(name)
	if err != nil {
		return err
	}
	if node == nil {
		logrus.Errorf("BUG: Longhorn node %v has been deleted", key)
		return nil
	}

	if node.DeletionTimestamp != nil {
		logrus.Errorf("BUG: Deleting Node %v", node.Name)
		return nc.ds.RemoveFinalizerForNode(node)
	}

	existingNode := node.DeepCopy()
	defer func() {
		// we're going to update volume assume things changes
		if err == nil && !reflect.DeepEqual(existingNode, node) {
			_, err = nc.ds.UpdateNode(node)
		}
		// requeue if it's conflict
		if apierrors.IsConflict(errors.Cause(err)) {
			logrus.Debugf("Requeue %v due to conflict", key)
			nc.enqueueNode(node)
			err = nil
		}
	}()

	// sync node state by manager pod
	managerPods, err := nc.ds.ListManagerPods()
	if err != nil {
		return err
	}
	for _, pod := range managerPods {
		if pod.Spec.NodeName == node.Name {
			switch pod.Status.Phase {
			case v1.PodRunning:
				node.Status.State = types.NodeStateUp
			default:
				node.Status.State = types.NodeStateDown
			}
		}
	}

	if nc.controllerID != node.Name {
		return nil
	}

	// sync disks status on current node
	if err := nc.syncDiskStatus(node); err != nil {
		return err
	}
	// sync mount propagation status on current node
	for _, pod := range managerPods {
		if pod.Spec.NodeName == node.Name {
			if err := nc.syncNodeStatus(pod, node); err != nil {
				return err
			}
		}
	}

	return nil
}

func (nc *NodeController) enqueueNode(node *longhorn.Node) {
	key, err := controller.KeyFunc(node)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", node, err))
		return
	}

	nc.queue.AddRateLimited(key)
}

func (nc *NodeController) enqueueSetting(setting *longhorn.Setting) {
	nodeList, err := nc.ds.ListNodes()
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get all nodes: %v ", err))
		return
	}

	for _, node := range nodeList {
		nc.enqueueNode(node)
	}
}

func (nc *NodeController) syncDiskStatus(node *longhorn.Node) error {
	diskMap := node.Spec.Disks
	diskStatusMap := map[string]types.DiskStatus{}

	// get all replicas which have been assigned to current node
	replicaDiskMap, err := nc.ds.ListReplicasByNode(node.Name)
	if err != nil {
		return err
	}

	// get settings of StorageMinimalAvailablePercentage
	minimalAvailablePercentage, err := nc.ds.GetSettingAsInt(types.SettingNameStorageMinimalAvailablePercentage)
	if err != nil {
		return err
	}

	for diskID, disk := range diskMap {
		diskStatus := types.DiskStatus{}
		// if there's no replica assigned to this disk
		if _, ok := replicaDiskMap[diskID]; !ok {
			diskStatus.StorageScheduled = 0
		} else {
			// calculate storage scheduled
			replicaArray := replicaDiskMap[diskID]
			var storageScheduled int64
			for _, replica := range replicaArray {
				storageScheduled += replica.Spec.VolumeSize
			}
			diskStatus.StorageScheduled = storageScheduled
			delete(replicaDiskMap, diskID)
		}
		// get disk available size
		diskInfo, err := util.GetDiskInfo(disk.Path)
		if err != nil {
			logrus.Errorf("Get disk information on node %v error: %v", node.Name, err)
		} else {
			diskStatus.StorageAvailable = diskInfo.StorageAvailable
		}

		// check disk pressure
		if diskInfo == nil ||
			diskInfo.StorageAvailable <= disk.StorageMaximum*minimalAvailablePercentage/100 {
			diskStatus.State = types.DiskStateUnschedulable
		} else {
			diskStatus.State = types.DiskStateSchedulable
		}

		diskStatusMap[diskID] = diskStatus
	}

	// if there's some replicas scheduled to wrong disks, write them to error log
	if len(replicaDiskMap) > 0 {
		eReplicas := []string{}
		for _, replicas := range replicaDiskMap {
			for _, replica := range replicas {
				eReplicas = append(eReplicas, replica.Name)
			}
		}
		logrus.Errorf("Warning: These replicas have been assigned to a disk no longer exist: %v", strings.Join(eReplicas, ", "))
	}

	node.Status.DiskStatus = diskStatusMap

	return nil
}

func (nc *NodeController) syncNodeStatus(pod *v1.Pod, node *longhorn.Node) error {
	// sync bidirectional mount propagation for node status to check whether the node could deploy CSI driver
	for _, mount := range pod.Spec.Containers[0].VolumeMounts {
		if mount.Name == types.LonghornSystemKey {
			mountPropagationStr := ""
			if mount.MountPropagation == nil {
				mountPropagationStr = "nil"
			} else {
				mountPropagationStr = string(*mount.MountPropagation)
			}
			if mount.MountPropagation == nil || *mount.MountPropagation != v1.MountPropagationBidirectional {
				if node.Status.MountPropagation {
					logrus.Debugf("The MountPropagation value %s is not expected from pod %s, node %s", mountPropagationStr, pod.ObjectMeta.Name, pod.Spec.NodeName)
				}
				node.Status.MountPropagation = false
			} else {
				node.Status.MountPropagation = true
			}
			break
		}
	}

	return nil
}
