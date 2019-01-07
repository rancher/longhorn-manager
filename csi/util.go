package csi

import (
	"fmt"
	"os"
	"strconv"

	"github.com/pkg/errors"
	"k8s.io/kubernetes/pkg/util/mount"

	longhornclient "github.com/rancher/longhorn-manager/client"
)

const (
	defaultStaleReplicaTimeout = 20
)

func getVolumeOptions(volOptions map[string]string) (*longhornclient.Volume, error) {
	vol := &longhornclient.Volume{}

	if staleReplicaTimeout, ok := volOptions["staleReplicaTimeout"]; ok {
		srt, err := strconv.Atoi(staleReplicaTimeout)
		if err != nil {
			return nil, errors.Wrap(err, "Invalid parameter staleReplicaTimeout")
		}
		vol.StaleReplicaTimeout = int64(srt)
	}
	if vol.StaleReplicaTimeout <= 0 {
		vol.StaleReplicaTimeout = defaultStaleReplicaTimeout
	}

	if numberOfReplicas, ok := volOptions["numberOfReplicas"]; ok {
		nor, err := strconv.Atoi(numberOfReplicas)
		if err != nil || nor < 0 {
			return nil, errors.Wrap(err, "Invalid parameter numberOfReplicas")
		}
		vol.NumberOfReplicas = int64(nor)
	}

	if fromBackup, ok := volOptions["fromBackup"]; ok {
		vol.FromBackup = fromBackup
	}

	if baseImage, ok := volOptions["baseImage"]; ok {
		vol.BaseImage = baseImage
	}

	return vol, nil
}

func isLikelyNotMountPointAttach(targetpath string) (bool, error) {
	notMnt, err := mount.New("").IsLikelyNotMountPoint(targetpath)
	if err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(targetpath, 0750)
			if err == nil {
				notMnt = true
			}
		}
	}
	return notMnt, err
}

func isLikelyNotMountPointDetach(targetpath string) (bool, error) {
	notMnt, err := mount.New("").IsLikelyNotMountPoint(targetpath)
	if err != nil {
		if os.IsNotExist(err) {
			return notMnt, fmt.Errorf("targetpath not found")
		}
	}
	return notMnt, err
}
