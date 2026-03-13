/*
Copyright 2026.

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

// Package fake provides a test double for oci.Interface.
// It records every call and returns configurable results, so controller tests
// can verify OCI interactions without hitting the real API.
package fake

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Call records a single OCI API call made by the controller.
type Call struct {
	Method string
	Args   []string
}

// Client is a thread-safe fake implementation of oci.Interface.
// Responses are configurable via the public fields; calls are recorded in Calls.
type Client struct {
	mu sync.Mutex

	// Calls is the ordered list of every OCI API call made.
	Calls []Call

	// VNICByInstance maps instanceOCID → vnicID returned by PrimaryVNICForInstance.
	VNICByInstance map[string]string

	// counter for generating unique fake OCIDs.
	counter atomic.Int64

	// PublicIPAddresses maps publicIPOCID → IP address returned by GetPublicIPAddress.
	// Populated automatically when CreateReservedPublicIP is called; override as needed.
	PublicIPAddresses map[string]string

	// Errors allows injecting errors for specific methods.
	// Key is the method name (e.g. "DeletePublicIP"); value is the error to return.
	Errors map[string]error
}

// New returns a ready-to-use fake Client.
func New() *Client {
	return &Client{
		VNICByInstance:    make(map[string]string),
		PublicIPAddresses: make(map[string]string),
		Errors:            make(map[string]error),
	}
}

func (c *Client) record(method string, args ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls = append(c.Calls, Call{Method: method, Args: args})
}

func (c *Client) nextID(prefix string) string {
	return fmt.Sprintf("ocid1.%s.fake.%d", prefix, c.counter.Add(1))
}

func (c *Client) err(method string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Errors[method]
}

// CallsFor returns all recorded calls for the given method name.
func (c *Client) CallsFor(method string) []Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Call
	for _, call := range c.Calls {
		if call.Method == method {
			out = append(out, call)
		}
	}
	return out
}

// Reset clears all recorded calls.
func (c *Client) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls = nil
}

// PrimaryVNICForInstance returns the pre-configured VNIC for the instance,
// or an error if none is configured.
func (c *Client) PrimaryVNICForInstance(_ context.Context, instanceOCID string) (string, error) {
	c.record("PrimaryVNICForInstance", instanceOCID)
	if err := c.err("PrimaryVNICForInstance"); err != nil {
		return "", err
	}
	c.mu.Lock()
	vnic, ok := c.VNICByInstance[instanceOCID]
	c.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("fake: no VNIC configured for instance %s", instanceOCID)
	}
	return vnic, nil
}

// CreateSecondaryPrivateIP records the call and returns a generated fake OCID + IP.
func (c *Client) CreateSecondaryPrivateIP(_ context.Context, vnicID string) (ocid, ipAddr string, err error) {
	c.record("CreateSecondaryPrivateIP", vnicID)
	if e := c.err("CreateSecondaryPrivateIP"); e != nil {
		return "", "", e
	}
	ocid = c.nextID("privateip")
	ipAddr = fmt.Sprintf("10.0.1.%d", c.counter.Load())
	return ocid, ipAddr, nil
}

// DeletePrivateIP records the call.
func (c *Client) DeletePrivateIP(_ context.Context, privateIPOCID string) error {
	c.record("DeletePrivateIP", privateIPOCID)
	return c.err("DeletePrivateIP")
}

// CreateReservedPublicIP records the call and returns a generated fake OCID + IP.
// The IP is stored in PublicIPAddresses so GetPublicIPAddress returns it.
func (c *Client) CreateReservedPublicIP(_ context.Context, privateIPOCID, displayName string) (ocid, ipAddr string, err error) {
	c.record("CreateReservedPublicIP", privateIPOCID, displayName)
	if e := c.err("CreateReservedPublicIP"); e != nil {
		return "", "", e
	}
	ocid = c.nextID("publicip")
	ipAddr = fmt.Sprintf("1.2.3.%d", c.counter.Load())
	c.mu.Lock()
	c.PublicIPAddresses[ocid] = ipAddr
	c.mu.Unlock()
	return ocid, ipAddr, nil
}

// ReassignPublicIP records the call.
func (c *Client) ReassignPublicIP(_ context.Context, publicIPOCID, newPrivateIPOCID string) error {
	c.record("ReassignPublicIP", publicIPOCID, newPrivateIPOCID)
	return c.err("ReassignPublicIP")
}

// GetPublicIPAddress returns the IP stored when CreateReservedPublicIP was called,
// or an error if the OCID is unknown.
func (c *Client) GetPublicIPAddress(_ context.Context, publicIPOCID string) (string, error) {
	c.record("GetPublicIPAddress", publicIPOCID)
	if e := c.err("GetPublicIPAddress"); e != nil {
		return "", e
	}
	c.mu.Lock()
	ip, ok := c.PublicIPAddresses[publicIPOCID]
	c.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("fake: unknown public IP OCID %s", publicIPOCID)
	}
	return ip, nil
}

// DeletePublicIP records the call.
func (c *Client) DeletePublicIP(_ context.Context, publicIPOCID string) error {
	c.record("DeletePublicIP", publicIPOCID)
	return c.err("DeletePublicIP")
}
