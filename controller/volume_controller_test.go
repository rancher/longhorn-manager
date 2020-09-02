package controller

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/controller"

	imutil "github.com/longhorn/longhorn-instance-manager/pkg/util"

	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/engineapi"
	"github.com/longhorn/longhorn-manager/types"
	"github.com/longhorn/longhorn-manager/util"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	lhfake "github.com/longhorn/longhorn-manager/k8s/pkg/client/clientset/versioned/fake"
	lhinformerfactory "github.com/longhorn/longhorn-manager/k8s/pkg/client/informers/externalversions"

	. "gopkg.in/check.v1"
)

func getVolumeLabelSelector(volumeName string) string {
	return "longhornvolume=" + volumeName
}

func setVolumeConditionWithoutTimestamp(originConditions map[string]types.Condition, conditionType string, conditionValue types.ConditionStatus, reason, message string) map[string]types.Condition {
	conditions := map[string]types.Condition{}
	if originConditions != nil {
		conditions = originConditions
	}
	condition := types.Condition{
		Type:    conditionType,
		Status:  conditionValue,
		Reason:  reason,
		Message: message,
	}
	conditions[conditionType] = condition
	return conditions
}

func initSettingsNameValue(name, value string) *longhorn.Setting {
	setting := &longhorn.Setting{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Setting: types.Setting{
			Value: value,
		},
	}
	return setting
}

func initSettings(ds *datastore.DataStore) {
	settingDefaultEngineImage := &longhorn.Setting{
		ObjectMeta: metav1.ObjectMeta{
			Name: string(types.SettingNameDefaultEngineImage),
		},
		Setting: types.Setting{
			Value: TestEngineImage,
		},
	}
	ds.CreateSetting(settingDefaultEngineImage)
}

func newTestVolumeController(lhInformerFactory lhinformerfactory.SharedInformerFactory, kubeInformerFactory informers.SharedInformerFactory,
	lhClient *lhfake.Clientset, kubeClient *fake.Clientset,
	controllerID string) *VolumeController {

	volumeInformer := lhInformerFactory.Longhorn().V1beta1().Volumes()
	engineInformer := lhInformerFactory.Longhorn().V1beta1().Engines()
	replicaInformer := lhInformerFactory.Longhorn().V1beta1().Replicas()
	engineImageInformer := lhInformerFactory.Longhorn().V1beta1().EngineImages()
	nodeInformer := lhInformerFactory.Longhorn().V1beta1().Nodes()
	diskInformer := lhInformerFactory.Longhorn().V1beta1().Disks()
	settingInformer := lhInformerFactory.Longhorn().V1beta1().Settings()
	imInformer := lhInformerFactory.Longhorn().V1beta1().InstanceManagers()

	podInformer := kubeInformerFactory.Core().V1().Pods()
	cronJobInformer := kubeInformerFactory.Batch().V1beta1().CronJobs()
	daemonSetInformer := kubeInformerFactory.Apps().V1().DaemonSets()
	deploymentInformer := kubeInformerFactory.Apps().V1().Deployments()
	persistentVolumeInformer := kubeInformerFactory.Core().V1().PersistentVolumes()
	persistentVolumeClaimInformer := kubeInformerFactory.Core().V1().PersistentVolumeClaims()
	kubeNodeInformer := kubeInformerFactory.Core().V1().Nodes()
	priorityClassInformer := kubeInformerFactory.Scheduling().V1().PriorityClasses()
	csiDriverInformer := kubeInformerFactory.Storage().V1beta1().CSIDrivers()
	pdbInformer := kubeInformerFactory.Policy().V1beta1().PodDisruptionBudgets()

	ds := datastore.NewDataStore(
		volumeInformer, engineInformer, replicaInformer,
		engineImageInformer, nodeInformer, diskInformer, settingInformer, imInformer,
		lhClient,
		podInformer, cronJobInformer, daemonSetInformer,
		deploymentInformer, persistentVolumeInformer,
		persistentVolumeClaimInformer, kubeNodeInformer, priorityClassInformer,
		csiDriverInformer,
		pdbInformer,
		kubeClient, TestNamespace)
	initSettings(ds)

	logger := logrus.StandardLogger()
	vc := NewVolumeController(logger,
		ds, scheme.Scheme,
		volumeInformer, engineInformer, replicaInformer,
		kubeClient, TestNamespace, controllerID, TestServiceAccount, TestManagerImage)

	fakeRecorder := record.NewFakeRecorder(100)
	vc.eventRecorder = fakeRecorder

	vc.vStoreSynced = alwaysReady
	vc.rStoreSynced = alwaysReady
	vc.eStoreSynced = alwaysReady
	vc.nowHandler = getTestNow

	return vc
}

type VolumeTestCase struct {
	volume   *longhorn.Volume
	engines  map[string]*longhorn.Engine
	replicas map[string]*longhorn.Replica
	nodes    []*longhorn.Node
	disks    []*longhorn.Disk

	expectVolume   *longhorn.Volume
	expectEngines  map[string]*longhorn.Engine
	expectReplicas map[string]*longhorn.Replica

	replicaNodeSoftAntiAffinity string
	volumeAutoSalvage           string
}

func (s *TestSuite) TestVolumeLifeCycle(c *C) {
	testBackupURL := engineapi.GetBackupURL(TestBackupTarget, TestBackupName, TestBackupVolumeName)

	var tc *VolumeTestCase
	testCases := map[string]*VolumeTestCase{}

	// We have to skip lister check for unit tests
	// Because the changes written through the API won't be reflected in the listers
	datastore.SkipListerCheck = true

	// normal volume creation
	tc = generateVolumeTestCaseTemplate()
	// default replica and engine objects will be copied by copyCurrentToExpect
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateCreating
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessUnknown
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	for _, r := range tc.expectReplicas {
		r.Spec.NodeID = TestNode1
		r.Spec.DiskID = TestDefaultDiskUUID1
	}
	tc.volume.Status.Conditions = map[string]types.Condition{}
	tc.engines = nil
	tc.replicas = nil
	// Set replica node soft anti-affinity
	tc.replicaNodeSoftAntiAffinity = "true"
	testCases["volume create"] = tc

	// unable to create volume because no node to schedule
	tc = generateVolumeTestCaseTemplate()
	for i := range tc.nodes {
		tc.nodes[i].Spec.AllowScheduling = false
	}
	tc.copyCurrentToExpect()
	// replicas and engine object would still be created
	tc.replicas = nil
	tc.engines = nil
	for _, r := range tc.expectReplicas {
		r.Spec.NodeID = ""
		r.Spec.DiskID = ""
		r.Spec.DataPath = ""
	}
	tc.expectVolume.Status.State = types.VolumeStateCreating
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessUnknown
	tc.expectVolume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.expectVolume.Status.Conditions,
		types.VolumeConditionTypeScheduled, types.ConditionStatusFalse, types.VolumeConditionReasonReplicaSchedulingFailure, "")
	testCases["volume create - replica scheduling failure"] = tc

	// after creation, volume in detached state
	tc = generateVolumeTestCaseTemplate()
	for _, e := range tc.engines {
		e.Status.CurrentState = types.InstanceStateStopped
	}
	for _, r := range tc.replicas {
		r.Status.CurrentState = types.InstanceStateStopped
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateDetached
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessUnknown
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	testCases["volume detached"] = tc

	// volume attaching, start replicas
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = TestNode1
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateAttaching
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	tc.expectVolume.Status.CurrentNodeID = tc.volume.Spec.NodeID
	// replicas will be started first
	// engine will be started only after all the replicas are running
	for _, r := range tc.expectReplicas {
		r.Spec.DesireState = types.InstanceStateRunning
	}
	testCases["volume attaching - start replicas"] = tc

	// volume attaching, start engine
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = TestNode1
	for _, r := range tc.replicas {
		r.Spec.DesireState = types.InstanceStateRunning
		r.Status.CurrentState = types.InstanceStateRunning
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateAttaching
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	tc.expectVolume.Status.CurrentNodeID = tc.volume.Spec.NodeID
	for _, e := range tc.expectEngines {
		e.Spec.NodeID = tc.expectVolume.Status.CurrentNodeID
		e.Spec.DesireState = types.InstanceStateRunning
	}
	for name, r := range tc.expectReplicas {
		//TODO update to r.Spec.AssociatedEngine
		for _, e := range tc.expectEngines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
		}
	}
	testCases["volume attaching - start controller"] = tc

	// volume attached
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = TestNode1

	for _, e := range tc.engines {
		e.Spec.NodeID = tc.volume.Spec.NodeID
		e.Spec.DesireState = types.InstanceStateRunning
		e.Status.CurrentState = types.InstanceStateRunning
		e.Status.IP = randomIP()
		e.Status.Port = randomPort()
		e.Status.Endpoint = "/dev/" + tc.volume.Name
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
	}
	for name, r := range tc.replicas {
		r.Spec.DesireState = types.InstanceStateRunning
		r.Status.CurrentState = types.InstanceStateRunning
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
		//TODO update to r.Spec.AssociatedEngine
		for _, e := range tc.engines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
			e.Status.ReplicaModeMap[name] = types.ReplicaModeRW
		}
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateAttached
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessHealthy
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	tc.expectVolume.Status.CurrentNodeID = tc.volume.Spec.NodeID
	for _, r := range tc.expectReplicas {
		r.Spec.HealthyAt = getTestNow()
	}
	testCases["volume attached"] = tc

	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Spec.FromBackup = testBackupURL
	tc.volume.Spec.Standby = false
	tc.volume.Spec.DisableFrontend = false
	tc.volume.Status.CurrentNodeID = TestNode1
	tc.volume.Status.State = types.VolumeStateAttaching
	tc.volume.Status.CurrentImage = TestEngineImage
	tc.volume.Status.RestoreRequired = true
	tc.volume.Status.RestoreInitiated = true
	tc.volume.Status.LastBackup = TestBackupName
	tc.volume.Status.FrontendDisabled = true
	tc.volume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.volume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusFalse, "", "")
	for _, e := range tc.engines {
		e.Spec.BackupVolume = TestBackupVolumeName
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
	}
	for _, r := range tc.replicas {
		r.Spec.HealthyAt = getTestNow()
		r.Spec.DesireState = types.InstanceStateRunning
		r.Spec.EngineImage = TestEngineImage
		r.Status.CurrentState = types.InstanceStateRunning
		r.Status.CurrentImage = TestEngineImage
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
	}
	tc.copyCurrentToExpect()
	for _, e := range tc.expectEngines {
		e.Spec.NodeID = TestNode1
		e.Spec.DesireState = types.InstanceStateRunning
		e.Spec.BackupVolume = TestBackupVolumeName
		e.Spec.RequestedBackupRestore = TestBackupName
		e.Spec.DisableFrontend = true
	}
	for name, r := range tc.expectReplicas {
		for _, e := range tc.expectEngines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
		}
	}
	// Set replica node soft anti-affinity
	tc.replicaNodeSoftAntiAffinity = "true"
	testCases["restored volume is automatically attaching after creation"] = tc

	// Newly restored volume changed from attaching to attached
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Spec.FromBackup = testBackupURL
	tc.volume.Spec.Standby = false
	tc.volume.Spec.DisableFrontend = false
	tc.volume.Status.CurrentNodeID = TestNode1
	tc.volume.Status.State = types.VolumeStateAttaching
	tc.volume.Status.LastBackup = TestBackupName
	tc.volume.Status.CurrentImage = TestEngineImage
	tc.volume.Status.FrontendDisabled = true
	tc.volume.Status.RestoreRequired = true
	tc.volume.Status.RestoreInitiated = true
	tc.volume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.volume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusFalse, "", "")
	for _, e := range tc.engines {
		e.Spec.NodeID = TestNode1
		e.Spec.DesireState = types.InstanceStateRunning
		e.Spec.BackupVolume = TestBackupVolumeName
		e.Spec.RequestedBackupRestore = TestBackupName
		e.Spec.DisableFrontend = true
		e.Status.OwnerID = TestNode1
		e.Status.CurrentState = types.InstanceStateRunning
		e.Status.IP = randomIP()
		e.Status.Port = randomPort()
		e.Status.Endpoint = "/dev/" + tc.volume.Name
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
	}
	for name, r := range tc.replicas {
		r.Spec.HealthyAt = getTestNow()
		r.Spec.DesireState = types.InstanceStateRunning
		r.Spec.EngineImage = TestEngineImage
		r.Status.CurrentState = types.InstanceStateRunning
		r.Status.CurrentImage = TestEngineImage
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
		for _, e := range tc.engines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
			e.Status.ReplicaModeMap[name] = types.ReplicaModeRW
		}
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateAttached
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessHealthy
	tc.expectVolume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.volume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusTrue, types.VolumeConditionReasonRestoreInProgress, "")
	testCases["newly restored volume attaching to attached"] = tc

	// Newly restored volume is waiting for restoration completed
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Spec.FromBackup = testBackupURL
	tc.volume.Spec.Standby = false
	tc.volume.Spec.DisableFrontend = false
	tc.volume.Status.CurrentNodeID = TestNode1
	tc.volume.Status.OwnerID = TestNode1
	tc.volume.Status.State = types.VolumeStateAttached
	tc.volume.Status.Robustness = types.VolumeRobustnessHealthy
	tc.volume.Status.CurrentImage = TestEngineImage
	tc.volume.Status.FrontendDisabled = true
	tc.volume.Status.RestoreRequired = true
	tc.volume.Status.RestoreInitiated = true
	tc.volume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.volume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusTrue, types.VolumeConditionReasonRestoreInProgress, "")
	for _, e := range tc.engines {
		e.Spec.NodeID = TestNode1
		e.Spec.DesireState = types.InstanceStateRunning
		e.Spec.BackupVolume = TestBackupVolumeName
		e.Spec.RequestedBackupRestore = TestBackupName
		e.Spec.DisableFrontend = true
		e.Status.OwnerID = TestNode1
		e.Status.CurrentState = types.InstanceStateRunning
		e.Status.IP = randomIP()
		e.Status.Port = randomPort()
		e.Status.Endpoint = "/dev/" + tc.volume.Name
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
		e.Status.LastRestoredBackup = ""
	}
	for name, r := range tc.replicas {
		r.Spec.HealthyAt = getTestNow()
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
		r.Spec.DesireState = types.InstanceStateRunning
		r.Status.CurrentState = types.InstanceStateRunning
		for _, e := range tc.engines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
			e.Status.ReplicaModeMap[name] = types.ReplicaModeRW
		}
	}
	tc.copyCurrentToExpect()
	testCases["newly restored volume is waiting for restoration completed"] = tc

	// try to detach newly restored volume after restoration completed
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Spec.FromBackup = testBackupURL
	tc.volume.Spec.Standby = false
	tc.volume.Spec.DisableFrontend = false
	tc.volume.Status.OwnerID = TestNode1
	tc.volume.Status.State = types.VolumeStateAttached
	tc.volume.Status.Robustness = types.VolumeRobustnessHealthy
	tc.volume.Status.CurrentNodeID = TestNode1
	tc.volume.Status.CurrentImage = TestEngineImage
	tc.volume.Status.FrontendDisabled = true
	tc.volume.Status.RestoreInitiated = true
	tc.volume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.volume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusTrue, types.VolumeConditionReasonRestoreInProgress, "")
	for _, e := range tc.engines {
		e.Spec.NodeID = TestNode1
		e.Spec.DesireState = types.InstanceStateRunning
		e.Spec.BackupVolume = TestBackupVolumeName
		e.Spec.RequestedBackupRestore = TestBackupName
		e.Spec.DisableFrontend = tc.volume.Status.FrontendDisabled
		e.Status.OwnerID = TestNode1
		e.Status.CurrentState = types.InstanceStateRunning
		e.Status.IP = randomIP()
		e.Status.Port = randomPort()
		e.Status.Endpoint = "/dev/" + tc.volume.Name
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
		e.Status.LastRestoredBackup = TestBackupName
	}
	for name, r := range tc.replicas {
		r.Spec.HealthyAt = getTestNow()
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
		r.Spec.DesireState = types.InstanceStateRunning
		r.Status.CurrentState = types.InstanceStateRunning
		for _, e := range tc.engines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
			e.Status.ReplicaModeMap[name] = types.ReplicaModeRW
		}
	}
	tc.copyCurrentToExpect()
	for _, e := range tc.expectEngines {
		e.Spec.NodeID = ""
		e.Spec.DesireState = types.InstanceStateStopped
		e.Spec.BackupVolume = ""
		e.Spec.RequestedBackupRestore = ""
	}
	tc.expectVolume.Spec.NodeID = ""
	tc.expectVolume.Spec.DisableFrontend = false
	tc.expectVolume.Status.CurrentNodeID = ""
	tc.expectVolume.Status.State = types.VolumeStateDetaching
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessUnknown
	tc.expectVolume.Status.FrontendDisabled = false
	tc.expectVolume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.expectVolume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusFalse, "", "")
	for _, r := range tc.expectReplicas {
		r.Spec.HealthyAt = getTestNow()
	}
	testCases["try to detach newly restored volume after restoration completed"] = tc

	// newly restored volume is being detaching after restoration completed
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Spec.FromBackup = testBackupURL
	tc.volume.Spec.Standby = false
	tc.volume.Spec.DisableFrontend = false
	tc.volume.Status.OwnerID = TestNode1
	tc.volume.Status.State = types.VolumeStateDetaching
	tc.volume.Status.Robustness = types.VolumeRobustnessUnknown
	tc.volume.Status.CurrentNodeID = ""
	tc.volume.Status.CurrentImage = TestEngineImage
	tc.volume.Status.FrontendDisabled = false
	tc.volume.Status.RestoreInitiated = true
	tc.volume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.volume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusFalse, "", "")
	for _, e := range tc.engines {
		e.Spec.NodeID = ""
		e.Spec.DesireState = types.InstanceStateStopped
		e.Spec.BackupVolume = ""
		e.Spec.RequestedBackupRestore = ""
		e.Status.OwnerID = TestNode1
		e.Status.CurrentState = types.InstanceStateStopped
		e.Status.IP = ""
		e.Status.Port = 0
		e.Status.Endpoint = ""
		e.Status.CurrentImage = ""
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
		e.Status.LastRestoredBackup = ""
	}
	for name, r := range tc.replicas {
		r.Spec.HealthyAt = getTestNow()
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
		r.Spec.DesireState = types.InstanceStateStopped
		r.Status.CurrentState = types.InstanceStateRunning
		for _, e := range tc.engines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
		}
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Spec.NodeID = ""
	tc.expectVolume.Spec.DisableFrontend = false
	tc.expectVolume.Status.CurrentNodeID = ""
	tc.expectVolume.Status.State = types.VolumeStateDetaching
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessUnknown
	for _, r := range tc.expectReplicas {
		r.Spec.HealthyAt = getTestNow()
	}
	testCases["newly restored volume is being detaching after restoration completed"] = tc

	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Spec.FromBackup = testBackupURL
	tc.volume.Spec.Standby = false
	tc.volume.Spec.DisableFrontend = false
	tc.volume.Spec.EngineImage = TestEngineImage
	tc.volume.Status.OwnerID = TestNode1
	tc.volume.Status.State = types.VolumeStateAttached
	tc.volume.Status.Robustness = types.VolumeRobustnessHealthy
	tc.volume.Status.CurrentNodeID = TestNode1
	tc.volume.Status.CurrentImage = TestEngineImage
	tc.volume.Status.FrontendDisabled = true
	tc.volume.Status.RestoreRequired = true
	tc.volume.Status.RestoreInitiated = true
	tc.volume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.volume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusTrue, types.VolumeConditionReasonRestoreInProgress, "")
	for _, e := range tc.engines {
		e.Spec.NodeID = TestNode1
		e.Spec.DesireState = types.InstanceStateRunning
		e.Spec.BackupVolume = TestBackupVolumeName
		e.Spec.RequestedBackupRestore = TestBackupName
		e.Spec.EngineImage = TestEngineImage
		e.Status.OwnerID = TestNode1
		e.Status.CurrentState = types.InstanceStateRunning
		e.Status.IP = randomIP()
		e.Status.Port = randomPort()
		e.Status.Endpoint = "/dev/" + tc.volume.Name
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
		e.Status.LastRestoredBackup = TestBackupName
		e.Status.CurrentImage = TestEngineImage
	}
	// Pick up one replica as the failed replica
	failedReplicaName := ""
	for name := range tc.replicas {
		failedReplicaName = name
		break
	}
	for name, r := range tc.replicas {
		r.Spec.NodeID = TestNode1
		r.Spec.HealthyAt = getTestNow()
		r.Spec.DesireState = types.InstanceStateRunning
		if name != failedReplicaName {
			r.Status.CurrentState = types.InstanceStateRunning
			r.Status.IP = randomIP()
			r.Status.Port = randomPort()
		} else {
			r.Status.CurrentState = types.InstanceStateError
		}
		for _, e := range tc.engines {
			e.Spec.DisableFrontend = true
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
			if name != failedReplicaName {
				e.Status.ReplicaModeMap[name] = types.ReplicaModeRW
			} else {
				e.Status.ReplicaModeMap[name] = types.ReplicaModeERR
			}
		}
	}
	tc.copyCurrentToExpect()
	newReplicaMap := map[string]*longhorn.Replica{}
	for name, r := range tc.replicas {
		if name != failedReplicaName {
			newReplicaMap[name] = r
		}
	}
	tc.expectReplicas = newReplicaMap
	for _, e := range tc.expectEngines {
		delete(e.Spec.ReplicaAddressMap, failedReplicaName)
		e.Spec.LogRequested = true
	}
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessDegraded
	testCases["the restored volume keeps and wait for the rebuild after the restoration completed"] = tc

	// try to update the volume as Faulted if all replicas failed to restore data
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Spec.FromBackup = testBackupURL
	tc.volume.Spec.Standby = true
	tc.volume.Spec.DisableFrontend = false
	tc.volume.Status.CurrentNodeID = TestNode1
	tc.volume.Status.FrontendDisabled = true
	tc.volume.Status.OwnerID = TestNode1
	tc.volume.Status.State = types.VolumeStateAttached
	tc.volume.Status.Robustness = types.VolumeRobustnessHealthy
	tc.volume.Status.CurrentImage = TestEngineImage
	tc.volume.Status.RestoreInitiated = true
	tc.volume.Status.IsStandby = true
	tc.volume.Status.LastBackup = TestBackupName
	tc.volume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.volume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusTrue, types.VolumeConditionReasonRestoreInProgress, "")
	for _, e := range tc.engines {
		e.Spec.NodeID = TestNode1
		e.Spec.DesireState = types.InstanceStateRunning
		e.Spec.BackupVolume = TestBackupVolumeName
		e.Spec.RequestedBackupRestore = TestBackupName
		e.Spec.DisableFrontend = tc.volume.Status.FrontendDisabled
		e.Status.OwnerID = TestNode1
		e.Status.CurrentState = types.InstanceStateRunning
		e.Status.IP = randomIP()
		e.Status.Port = randomPort()
		e.Status.Endpoint = "/dev/" + tc.volume.Name
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
		e.Status.LastRestoredBackup = TestBackupName
	}
	for name, r := range tc.replicas {
		r.Spec.HealthyAt = getTestNow()
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
		r.Spec.DesireState = types.InstanceStateRunning
		r.Status.CurrentState = types.InstanceStateRunning
		for _, e := range tc.engines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
			e.Status.ReplicaModeMap[name] = types.ReplicaModeRW
			if e.Status.RestoreStatus == nil {
				e.Status.RestoreStatus = map[string]*types.RestoreStatus{}
			}
			e.Status.RestoreStatus[name] = &types.RestoreStatus{
				Error: "Test restore error",
			}
		}
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Spec.NodeID = ""
	tc.expectVolume.Spec.DisableFrontend = false
	tc.expectVolume.Status.CurrentNodeID = ""
	tc.expectVolume.Status.State = types.VolumeStateDetaching
	tc.expectVolume.Status.FrontendDisabled = true
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessFaulted
	tc.expectVolume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.expectVolume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusFalse, types.VolumeConditionReasonRestoreFailure, "All replica restore failed and the volume became Faulted")
	for _, e := range tc.expectEngines {
		e.Spec.NodeID = ""
		e.Spec.DesireState = types.InstanceStateStopped
		e.Spec.LogRequested = true
		e.Spec.RequestedBackupRestore = ""
	}
	for _, r := range tc.expectReplicas {
		r.Spec.FailedAt = getTestNow()
		r.Spec.DesireState = types.InstanceStateStopped
		r.Spec.LogRequested = true
	}
	testCases["newly restored volume becomes faulted after all replica error"] = tc

	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Spec.FromBackup = testBackupURL
	tc.volume.Spec.Standby = true
	tc.volume.Spec.DisableFrontend = false
	tc.volume.Status.CurrentNodeID = TestNode1
	tc.volume.Status.State = types.VolumeStateAttaching
	tc.volume.Status.CurrentImage = TestEngineImage
	tc.volume.Status.FrontendDisabled = true
	tc.volume.Status.RestoreRequired = true
	tc.volume.Status.RestoreInitiated = true
	tc.volume.Status.IsStandby = true
	tc.volume.Status.LastBackup = TestBackupName
	tc.volume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.volume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusTrue, types.VolumeConditionReasonRestoreInProgress, "")
	for _, e := range tc.engines {
		e.Spec.RequestedBackupRestore = TestBackupName
		e.Spec.BackupVolume = TestBackupVolumeName
	}
	for _, r := range tc.replicas {
		r.Spec.HealthyAt = ""
	}
	tc.copyCurrentToExpect()
	for _, e := range tc.expectEngines {
		e.Spec.RequestedBackupRestore = TestBackupName
	}
	for _, r := range tc.expectReplicas {
		r.Spec.DesireState = types.InstanceStateRunning
	}
	testCases["new standby volume is automatically attaching"] = tc

	// New standby volume changed from attaching to attached, and it's not automatically detached
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Spec.FromBackup = testBackupURL
	tc.volume.Spec.Standby = true
	tc.volume.Spec.DisableFrontend = false
	tc.volume.Status.CurrentNodeID = TestNode1
	tc.volume.Status.State = types.VolumeStateAttaching
	tc.volume.Status.CurrentImage = TestEngineImage
	tc.volume.Status.LastBackup = TestBackupName
	tc.volume.Status.FrontendDisabled = true
	tc.volume.Status.RestoreRequired = true
	tc.volume.Status.RestoreInitiated = true
	tc.volume.Status.IsStandby = true
	tc.volume.Status.Conditions = setVolumeConditionWithoutTimestamp(tc.volume.Status.Conditions,
		types.VolumeConditionTypeRestore, types.ConditionStatusTrue, types.VolumeConditionReasonRestoreInProgress, "")
	for _, e := range tc.engines {
		e.Spec.NodeID = tc.volume.Status.CurrentNodeID
		e.Spec.DesireState = types.InstanceStateRunning
		e.Spec.BackupVolume = TestBackupVolumeName
		e.Spec.RequestedBackupRestore = TestBackupName
		e.Spec.DisableFrontend = tc.volume.Status.FrontendDisabled
		e.Status.CurrentState = types.InstanceStateRunning
		e.Status.IP = randomIP()
		e.Status.Port = randomPort()
		e.Status.Endpoint = "/dev/" + tc.volume.Name
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
		e.Status.LastRestoredBackup = TestBackupName
	}
	for name, r := range tc.replicas {
		r.Spec.HealthyAt = getTestNow()
		r.Spec.DesireState = types.InstanceStateRunning
		r.Status.CurrentState = types.InstanceStateRunning
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
		for _, e := range tc.engines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
			e.Status.ReplicaModeMap[name] = types.ReplicaModeRW
		}
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateAttached
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessHealthy
	testCases["standby volume is not automatically detached"] = tc

	// volume detaching - stop engine
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Status.CurrentNodeID = ""
	for _, e := range tc.engines {
		e.Spec.NodeID = TestNode1
		e.Spec.DesireState = types.InstanceStateRunning
		e.Status.CurrentState = types.InstanceStateRunning
		e.Status.IP = randomIP()
		e.Status.Port = randomPort()
		e.Status.Endpoint = "/dev/" + tc.volume.Name
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
	}
	for name, r := range tc.replicas {
		r.Spec.DesireState = types.InstanceStateRunning
		r.Spec.HealthyAt = getTestNow()
		r.Status.CurrentState = types.InstanceStateRunning
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
		//TODO update to r.Spec.AssociatedEngine
		for _, e := range tc.engines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
			e.Status.ReplicaModeMap[name] = types.ReplicaModeRW
		}
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateDetaching
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessUnknown
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	for _, e := range tc.expectEngines {
		e.Spec.NodeID = ""
		e.Spec.DesireState = types.InstanceStateStopped
	}
	testCases["volume detaching - stop engine"] = tc

	// volume detaching - stop replicas
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Status.CurrentNodeID = ""
	for _, e := range tc.engines {
		e.Spec.NodeID = ""
		e.Status.CurrentState = types.InstanceStateStopped
	}
	for name, r := range tc.replicas {
		r.Spec.DesireState = types.InstanceStateRunning
		r.Spec.HealthyAt = getTestNow()
		r.Status.CurrentState = types.InstanceStateRunning
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
		//TODO update to r.Spec.AssociatedEngine
		for _, e := range tc.engines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
		}
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateDetaching
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessUnknown
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	for _, r := range tc.expectReplicas {
		r.Spec.DesireState = types.InstanceStateStopped
	}
	testCases["volume detaching - stop replicas"] = tc

	// volume deleting
	tc = generateVolumeTestCaseTemplate()
	now := metav1.NewTime(time.Now())
	tc.volume.SetDeletionTimestamp(&now)
	tc.volume.Status.Conditions = map[string]types.Condition{}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateDeleting
	tc.expectEngines = nil
	tc.expectReplicas = nil
	testCases["volume deleting"] = tc

	// volume attaching, start replicas, one node down
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = TestNode1
	tc.volume.Status.CurrentNodeID = TestNode1
	tc.nodes[1] = newNode(TestNode2, TestNamespace, TestDefaultDiskUUID2, false, types.ConditionStatusFalse, string(types.NodeConditionReasonKubernetesNodeGone))
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateAttaching
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	expectRs := map[string]*longhorn.Replica{}
	for _, r := range tc.expectReplicas {
		if r.Spec.NodeID != TestNode2 {
			r.Spec.DesireState = types.InstanceStateRunning
			expectRs[r.Name] = r
		}
	}
	tc.expectReplicas = expectRs
	testCases["volume attaching - start replicas - node failed"] = tc

	// Disable revision counter
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = TestNode1
	tc.volume.Status.OwnerID = TestNode1
	tc.volume.Status.CurrentNodeID = TestNode1

	// Since default node 2 is scheduling disabled,
	// enable node soft anti-affinity for 2 replicas.
	tc.replicaNodeSoftAntiAffinity = "true"
	tc.volume.Spec.RevisionCounterDisabled = true
	tc.copyCurrentToExpect()
	tc.engines = nil
	tc.replicas = nil

	tc.expectVolume.Status.State = types.VolumeStateAttaching
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	tc.expectVolume.Status.CurrentNodeID = tc.volume.Spec.NodeID
	expectEs := map[string]*longhorn.Engine{}
	for _, e := range tc.expectEngines {
		e.Spec.RevisionCounterDisabled = true
		expectEs[e.Name] = e
	}
	tc.expectEngines = expectEs
	expectRs = map[string]*longhorn.Replica{}
	for _, r := range tc.expectReplicas {
		r.Spec.DesireState = types.InstanceStateRunning
		r.Spec.RevisionCounterDisabled = true
		expectRs[r.Name] = r
	}
	tc.expectReplicas = expectRs

	testCases["volume revision counter disabled - engine and replica revision counter disabled"] = tc

	// Salvage Requested
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = TestNode1
	tc.volume.Spec.RevisionCounterDisabled = true
	tc.volume.Status.State = types.VolumeStateDetached
	tc.volume.Status.Robustness = types.VolumeRobustnessFaulted
	tc.volume.Status.CurrentNodeID = ""
	tc.volumeAutoSalvage = "true"

	for _, e := range tc.engines {
		e.Status.CurrentState = types.InstanceStateStopped
		e.Status.ReplicaModeMap = map[string]types.ReplicaMode{}
		e.Status.SalvageExecuted = false
		e.Spec.DesireState = types.InstanceStateStopped
	}
	for _, r := range tc.replicas {
		r.Status.CurrentState = types.InstanceStateStopped
		r.Spec.HealthyAt = getTestNow()
		r.Spec.FailedAt = getTestNow()
		r.Spec.DesireState = types.InstanceStateStopped
	}

	tc.copyCurrentToExpect()

	expectEs = map[string]*longhorn.Engine{}
	for _, e := range tc.expectEngines {
		e.Spec.SalvageRequested = true
		expectEs[e.Name] = e
	}
	tc.expectEngines = expectEs

	expectRs = map[string]*longhorn.Replica{}
	for _, r := range tc.expectReplicas {
		r.Spec.DesireState = types.InstanceStateStopped
		r.Spec.FailedAt = ""
		expectRs[r.Name] = r
	}
	tc.expectReplicas = expectRs

	tc.expectVolume.Status.State = types.VolumeStateDetached
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	tc.expectVolume.Status.CurrentNodeID = TestNode1
	tc.expectVolume.Status.PendingNodeID = ""
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessUnknown
	tc.expectVolume.Status.RemountRequestedAt = getTestNow()

	testCases["volume salvage requested - all replica failed"] = tc

	s.runTestCases(c, testCases)

	// volume attaching, start replicas, manager restart
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = TestNode1
	tc.volume.Status.CurrentNodeID = TestNode1
	tc.nodes[1] = newNode(TestNode2, TestNamespace, TestDefaultDiskUUID2, false, types.ConditionStatusFalse, string(types.NodeConditionReasonManagerPodDown))
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateAttaching
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	expectRs = map[string]*longhorn.Replica{}
	for _, r := range tc.expectReplicas {
		r.Spec.DesireState = types.InstanceStateRunning
		expectRs[r.Name] = r
	}
	tc.expectReplicas = expectRs
	testCases["volume attaching - start replicas - manager down"] = tc
	s.runTestCases(c, testCases)

	// restoring volume reattaching, stop replicas
	tc = generateVolumeTestCaseTemplate()
	tc.volume.Spec.NodeID = ""
	tc.volume.Status.CurrentNodeID = ""
	tc.volume.Status.PendingNodeID = TestNode1
	tc.volume.Status.State = types.VolumeStateDetaching
	for _, e := range tc.engines {
		e.Spec.NodeID = ""
		e.Status.CurrentState = types.InstanceStateStopped
	}
	for name, r := range tc.replicas {
		r.Spec.DesireState = types.InstanceStateRunning
		r.Spec.HealthyAt = getTestNow()
		r.Status.CurrentState = types.InstanceStateRunning
		r.Status.IP = randomIP()
		r.Status.Port = randomPort()
		for _, e := range tc.engines {
			e.Spec.ReplicaAddressMap[name] = imutil.GetURL(r.Status.IP, r.Status.Port)
		}
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.State = types.VolumeStateDetaching
	tc.expectVolume.Status.Robustness = types.VolumeRobustnessUnknown
	tc.expectVolume.Status.CurrentImage = tc.volume.Spec.EngineImage
	for _, r := range tc.expectReplicas {
		r.Spec.DesireState = types.InstanceStateStopped
	}
	testCases["restoring volume reattaching - stop replicas"] = tc
}

func newVolume(name string, replicaCount int) *longhorn.Volume {
	return &longhorn.Volume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Finalizers: []string{
				longhorn.SchemeGroupVersion.Group,
			},
		},
		Spec: types.VolumeSpec{
			Frontend:            types.VolumeFrontendBlockDev,
			NumberOfReplicas:    replicaCount,
			Size:                TestVolumeSize,
			StaleReplicaTimeout: TestVolumeStaleTimeout,
			EngineImage:         TestEngineImage,
		},
		Status: types.VolumeStatus{
			OwnerID: TestOwnerID1,
			Conditions: map[string]types.Condition{
				types.VolumeConditionTypeScheduled: {
					Type:   string(types.VolumeConditionTypeScheduled),
					Status: types.ConditionStatusTrue,
				},
				types.VolumeConditionTypeRestore: {
					Type:   string(types.VolumeConditionTypeRestore),
					Status: types.ConditionStatusFalse,
				},
			},
		},
	}
}

func newEngineForVolume(v *longhorn.Volume) *longhorn.Engine {
	return &longhorn.Engine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      v.Name + "-e-" + util.RandomID(),
			Namespace: TestNamespace,
			Labels: map[string]string{
				"longhornvolume": v.Name,
			},
		},
		Spec: types.EngineSpec{
			InstanceSpec: types.InstanceSpec{
				VolumeName:  v.Name,
				VolumeSize:  v.Spec.Size,
				EngineImage: TestEngineImage,
				DesireState: types.InstanceStateStopped,
			},
			Frontend:                  types.VolumeFrontendBlockDev,
			ReplicaAddressMap:         map[string]string{},
			UpgradedReplicaAddressMap: map[string]string{},
		},
	}
}

func newReplicaForVolume(v *longhorn.Volume, e *longhorn.Engine, nodeID, diskID string) *longhorn.Replica {
	replicaName := v.Name + "-r-" + util.RandomID()
	return &longhorn.Replica{
		ObjectMeta: metav1.ObjectMeta{
			Name: replicaName,
			Labels: map[string]string{
				"longhornvolume":      v.Name,
				types.LonghornNodeKey: nodeID,
				types.LonghornDiskKey: diskID,
			},
		},
		Spec: types.ReplicaSpec{
			InstanceSpec: types.InstanceSpec{
				VolumeName:  v.Name,
				VolumeSize:  v.Spec.Size,
				NodeID:      nodeID,
				EngineImage: TestEngineImage,
				DesireState: types.InstanceStateStopped,
			},
			EngineName: e.Name,
			DiskID:     diskID,
			DataPath:   filepath.Join(TestDefaultDataPath, "/replicas", replicaName),
			Active:     true,
		},
	}
}

func generateVolumeTestCaseTemplate() *VolumeTestCase {
	volume := newVolume(TestVolumeName, 2)
	engine := newEngineForVolume(volume)
	replica1 := newReplicaForVolume(volume, engine, TestNode1, TestDefaultDiskUUID1)
	replica2 := newReplicaForVolume(volume, engine, TestNode2, TestDefaultDiskUUID2)
	node1 := newNode(TestNode1, TestNamespace, TestDefaultDiskUUID1, true, types.ConditionStatusTrue, "")
	disk1 := newDisk(TestDefaultDiskUUID1, TestNamespace, TestNode1)
	node2 := newNode(TestNode2, TestNamespace, TestDefaultDiskUUID2, false, types.ConditionStatusTrue, "")
	disk2 := newDisk(TestDefaultDiskUUID2, TestNamespace, TestNode2)

	return &VolumeTestCase{
		volume: volume,
		engines: map[string]*longhorn.Engine{
			engine.Name: engine,
		},
		replicas: map[string]*longhorn.Replica{
			replica1.Name: replica1,
			replica2.Name: replica2,
		},
		nodes: []*longhorn.Node{
			node1,
			node2,
		},
		disks: []*longhorn.Disk{
			disk1,
			disk2,
		},

		expectVolume:   nil,
		expectEngines:  map[string]*longhorn.Engine{},
		expectReplicas: map[string]*longhorn.Replica{},
	}
}

func (tc *VolumeTestCase) copyCurrentToExpect() {
	tc.expectVolume = tc.volume.DeepCopy()
	for n, e := range tc.engines {
		tc.expectEngines[n] = e.DeepCopy()
	}
	for n, r := range tc.replicas {
		tc.expectReplicas[n] = r.DeepCopy()
	}
}

func (s *TestSuite) runTestCases(c *C, testCases map[string]*VolumeTestCase) {
	for name, tc := range testCases {
		var err error
		fmt.Printf("testing %v\n", name)

		kubeClient := fake.NewSimpleClientset()
		kubeInformerFactory := informers.NewSharedInformerFactory(kubeClient, controller.NoResyncPeriodFunc())

		lhClient := lhfake.NewSimpleClientset()
		lhInformerFactory := lhinformerfactory.NewSharedInformerFactory(lhClient, controller.NoResyncPeriodFunc())
		vIndexer := lhInformerFactory.Longhorn().V1beta1().Volumes().Informer().GetIndexer()
		eIndexer := lhInformerFactory.Longhorn().V1beta1().Engines().Informer().GetIndexer()
		rIndexer := lhInformerFactory.Longhorn().V1beta1().Replicas().Informer().GetIndexer()
		nIndexer := lhInformerFactory.Longhorn().V1beta1().Nodes().Informer().GetIndexer()
		dIndexer := lhInformerFactory.Longhorn().V1beta1().Disks().Informer().GetIndexer()

		pIndexer := kubeInformerFactory.Core().V1().Pods().Informer().GetIndexer()
		knIndexer := kubeInformerFactory.Core().V1().Nodes().Informer().GetIndexer()
		sIndexer := lhInformerFactory.Longhorn().V1beta1().Settings().Informer().GetIndexer()

		vc := newTestVolumeController(lhInformerFactory, kubeInformerFactory, lhClient, kubeClient, TestOwnerID1)

		// Need to create daemon pod for node
		daemon1 := newDaemonPod(v1.PodRunning, TestDaemon1, TestNamespace, TestNode1, TestIP1, nil)
		p, err := kubeClient.CoreV1().Pods(TestNamespace).Create(daemon1)
		c.Assert(err, IsNil)
		pIndexer.Add(p)
		daemon2 := newDaemonPod(v1.PodRunning, TestDaemon2, TestNamespace, TestNode2, TestIP2, nil)
		p, err = kubeClient.CoreV1().Pods(TestNamespace).Create(daemon2)
		c.Assert(err, IsNil)
		pIndexer.Add(p)

		ei, err := lhClient.LonghornV1beta1().EngineImages(TestNamespace).Create(newEngineImage(TestEngineImage, types.EngineImageStateReady))
		c.Assert(err, IsNil)
		eiIndexer := lhInformerFactory.Longhorn().V1beta1().EngineImages().Informer().GetIndexer()
		err = eiIndexer.Add(ei)
		c.Assert(err, IsNil)

		// Set replica node soft anti-affinity setting
		if tc.replicaNodeSoftAntiAffinity != "" {
			s := initSettingsNameValue(
				string(types.SettingNameReplicaSoftAntiAffinity),
				tc.replicaNodeSoftAntiAffinity)
			setting, err :=
				lhClient.LonghornV1beta1().Settings(TestNamespace).Create(s)
			c.Assert(err, IsNil)
			sIndexer.Add(setting)
		}

		// Set auto salvage setting
		if tc.volumeAutoSalvage != "" {
			s := initSettingsNameValue(
				string(types.SettingNameAutoSalvage),
				tc.volumeAutoSalvage)
			setting, err :=
				lhClient.LonghornV1beta1().Settings(TestNamespace).Create(s)
			c.Assert(err, IsNil)
			sIndexer.Add(setting)
		}

		// need to create default node and the corresponding disk
		for _, node := range tc.nodes {
			n, err := lhClient.LonghornV1beta1().Nodes(TestNamespace).Create(node)
			c.Assert(err, IsNil)
			c.Assert(n, NotNil)
			nIndexer.Add(n)

			knodeCondition := v1.ConditionTrue
			if node.Status.Conditions[types.NodeConditionTypeReady].Status != types.ConditionStatusTrue {
				knodeCondition = v1.ConditionFalse
			}
			knode := newKubernetesNode(node.Name, knodeCondition, v1.ConditionFalse, v1.ConditionFalse, v1.ConditionFalse, v1.ConditionFalse, v1.ConditionFalse, v1.ConditionTrue)
			kn, err := kubeClient.CoreV1().Nodes().Create(knode)
			c.Assert(err, IsNil)
			knIndexer.Add(kn)
		}
		for _, disk := range tc.disks {
			d, err := lhClient.LonghornV1beta1().Disks(TestNamespace).Create(disk)
			c.Assert(err, IsNil)
			c.Assert(d, NotNil)
			dIndexer.Add(d)
		}

		// Need to put it into both fakeclientset and Indexer
		v, err := lhClient.LonghornV1beta1().Volumes(TestNamespace).Create(tc.volume)
		c.Assert(err, IsNil)
		err = vIndexer.Add(v)
		c.Assert(err, IsNil)

		if tc.engines != nil {
			for _, e := range tc.engines {
				e, err := lhClient.LonghornV1beta1().Engines(TestNamespace).Create(e)
				c.Assert(err, IsNil)
				err = eIndexer.Add(e)
				c.Assert(err, IsNil)
			}
		}

		if tc.replicas != nil {
			for _, r := range tc.replicas {
				r, err = lhClient.LonghornV1beta1().Replicas(TestNamespace).Create(r)
				c.Assert(err, IsNil)
				err = rIndexer.Add(r)
				c.Assert(err, IsNil)
			}
		}

		err = vc.syncVolume(getKey(v, c))
		c.Assert(err, IsNil)

		retV, err := lhClient.LonghornV1beta1().Volumes(TestNamespace).Get(v.Name, metav1.GetOptions{})
		c.Assert(err, IsNil)
		c.Assert(retV.Spec, DeepEquals, tc.expectVolume.Spec)
		// mask timestamps
		for ctype, condition := range retV.Status.Conditions {
			condition.LastTransitionTime = ""
			retV.Status.Conditions[ctype] = condition
		}
		c.Assert(retV.Status, DeepEquals, tc.expectVolume.Status)

		retEs, err := lhClient.LonghornV1beta1().Engines(TestNamespace).List(metav1.ListOptions{LabelSelector: getVolumeLabelSelector(v.Name)})
		c.Assert(err, IsNil)
		c.Assert(retEs.Items, HasLen, len(tc.expectEngines))
		for _, retE := range retEs.Items {
			if tc.engines == nil {
				// test creation, name would be different
				var expectE *longhorn.Engine
				for _, expectE = range tc.expectEngines {
					break
				}
				c.Assert(retE.Spec, DeepEquals, expectE.Spec)
				c.Assert(retE.Status, DeepEquals, expectE.Status)
			} else {
				c.Assert(retE.Spec, DeepEquals, tc.expectEngines[retE.Name].Spec)
				c.Assert(retE.Status, DeepEquals, tc.expectEngines[retE.Name].Status)
			}
		}

		retRs, err := lhClient.LonghornV1beta1().Replicas(TestNamespace).List(metav1.ListOptions{LabelSelector: getVolumeLabelSelector(v.Name)})
		c.Assert(err, IsNil)
		c.Assert(retRs.Items, HasLen, len(tc.expectReplicas))
		for _, retR := range retRs.Items {
			if tc.replicas == nil {
				// test creation, name would be different
				var expectR *longhorn.Replica
				for _, expectR = range tc.expectReplicas {
					break
				}
				if expectR.Spec.NodeID != "" {
					// validate DataPath and NodeID of replica have been set in scheduler
					c.Assert(retR.Spec.DataPath, Not(Equals), "")
					c.Assert(retR.Spec.NodeID, Not(Equals), "")
					c.Assert(retR.Spec.DiskID, Not(Equals), "")
					c.Assert(retR.Spec.NodeID, Equals, TestNode1)
				} else {
					// not schedulable
					c.Assert(retR.Spec.DataPath, Equals, "")
					c.Assert(retR.Spec.NodeID, Equals, "")
					c.Assert(retR.Spec.DiskID, Equals, "")
				}
				c.Assert(retR.Status, DeepEquals, expectR.Status)
			} else {
				c.Assert(retR.Spec, DeepEquals, tc.expectReplicas[retR.Name].Spec)
				c.Assert(retR.Status, DeepEquals, tc.expectReplicas[retR.Name].Status)
			}
		}
	}
}
