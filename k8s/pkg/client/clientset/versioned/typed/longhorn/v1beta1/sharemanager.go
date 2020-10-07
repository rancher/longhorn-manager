/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Code generated by client-gen. DO NOT EDIT.

package v1beta1

import (
	"time"

	v1beta1 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	scheme "github.com/longhorn/longhorn-manager/k8s/pkg/client/clientset/versioned/scheme"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	rest "k8s.io/client-go/rest"
)

// ShareManagersGetter has a method to return a ShareManagerInterface.
// A group's client should implement this interface.
type ShareManagersGetter interface {
	ShareManagers(namespace string) ShareManagerInterface
}

// ShareManagerInterface has methods to work with ShareManager resources.
type ShareManagerInterface interface {
	Create(*v1beta1.ShareManager) (*v1beta1.ShareManager, error)
	Update(*v1beta1.ShareManager) (*v1beta1.ShareManager, error)
	UpdateStatus(*v1beta1.ShareManager) (*v1beta1.ShareManager, error)
	Delete(name string, options *v1.DeleteOptions) error
	DeleteCollection(options *v1.DeleteOptions, listOptions v1.ListOptions) error
	Get(name string, options v1.GetOptions) (*v1beta1.ShareManager, error)
	List(opts v1.ListOptions) (*v1beta1.ShareManagerList, error)
	Watch(opts v1.ListOptions) (watch.Interface, error)
	Patch(name string, pt types.PatchType, data []byte, subresources ...string) (result *v1beta1.ShareManager, err error)
	ShareManagerExpansion
}

// shareManagers implements ShareManagerInterface
type shareManagers struct {
	client rest.Interface
	ns     string
}

// newShareManagers returns a ShareManagers
func newShareManagers(c *LonghornV1beta1Client, namespace string) *shareManagers {
	return &shareManagers{
		client: c.RESTClient(),
		ns:     namespace,
	}
}

// Get takes name of the shareManager, and returns the corresponding shareManager object, and an error if there is any.
func (c *shareManagers) Get(name string, options v1.GetOptions) (result *v1beta1.ShareManager, err error) {
	result = &v1beta1.ShareManager{}
	err = c.client.Get().
		Namespace(c.ns).
		Resource("sharemanagers").
		Name(name).
		VersionedParams(&options, scheme.ParameterCodec).
		Do().
		Into(result)
	return
}

// List takes label and field selectors, and returns the list of ShareManagers that match those selectors.
func (c *shareManagers) List(opts v1.ListOptions) (result *v1beta1.ShareManagerList, err error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result = &v1beta1.ShareManagerList{}
	err = c.client.Get().
		Namespace(c.ns).
		Resource("sharemanagers").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Do().
		Into(result)
	return
}

// Watch returns a watch.Interface that watches the requested shareManagers.
func (c *shareManagers) Watch(opts v1.ListOptions) (watch.Interface, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	opts.Watch = true
	return c.client.Get().
		Namespace(c.ns).
		Resource("sharemanagers").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Watch()
}

// Create takes the representation of a shareManager and creates it.  Returns the server's representation of the shareManager, and an error, if there is any.
func (c *shareManagers) Create(shareManager *v1beta1.ShareManager) (result *v1beta1.ShareManager, err error) {
	result = &v1beta1.ShareManager{}
	err = c.client.Post().
		Namespace(c.ns).
		Resource("sharemanagers").
		Body(shareManager).
		Do().
		Into(result)
	return
}

// Update takes the representation of a shareManager and updates it. Returns the server's representation of the shareManager, and an error, if there is any.
func (c *shareManagers) Update(shareManager *v1beta1.ShareManager) (result *v1beta1.ShareManager, err error) {
	result = &v1beta1.ShareManager{}
	err = c.client.Put().
		Namespace(c.ns).
		Resource("sharemanagers").
		Name(shareManager.Name).
		Body(shareManager).
		Do().
		Into(result)
	return
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().

func (c *shareManagers) UpdateStatus(shareManager *v1beta1.ShareManager) (result *v1beta1.ShareManager, err error) {
	result = &v1beta1.ShareManager{}
	err = c.client.Put().
		Namespace(c.ns).
		Resource("sharemanagers").
		Name(shareManager.Name).
		SubResource("status").
		Body(shareManager).
		Do().
		Into(result)
	return
}

// Delete takes name of the shareManager and deletes it. Returns an error if one occurs.
func (c *shareManagers) Delete(name string, options *v1.DeleteOptions) error {
	return c.client.Delete().
		Namespace(c.ns).
		Resource("sharemanagers").
		Name(name).
		Body(options).
		Do().
		Error()
}

// DeleteCollection deletes a collection of objects.
func (c *shareManagers) DeleteCollection(options *v1.DeleteOptions, listOptions v1.ListOptions) error {
	var timeout time.Duration
	if listOptions.TimeoutSeconds != nil {
		timeout = time.Duration(*listOptions.TimeoutSeconds) * time.Second
	}
	return c.client.Delete().
		Namespace(c.ns).
		Resource("sharemanagers").
		VersionedParams(&listOptions, scheme.ParameterCodec).
		Timeout(timeout).
		Body(options).
		Do().
		Error()
}

// Patch applies the patch and returns the patched shareManager.
func (c *shareManagers) Patch(name string, pt types.PatchType, data []byte, subresources ...string) (result *v1beta1.ShareManager, err error) {
	result = &v1beta1.ShareManager{}
	err = c.client.Patch(pt).
		Namespace(c.ns).
		Resource("sharemanagers").
		SubResource(subresources...).
		Name(name).
		Body(data).
		Do().
		Into(result)
	return
}
