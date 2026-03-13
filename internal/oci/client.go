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

// Package oci wraps the OCI Go SDK for the operations pivot-controller needs:
// secondary private IPs and reserved public IPs on VNICs.
package oci

import (
	"context"
	"fmt"
	"strings"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// Interface is the OCI operations contract used by the controller.
// The concrete Client uses instance principal auth; fake.Client is the test double.
type Interface interface {
	// PrimaryVNICForInstance returns the OCID of the primary VNIC attached to an OCI instance.
	PrimaryVNICForInstance(ctx context.Context, instanceOCID string) (string, error)
	// CreateSecondaryPrivateIP creates a secondary private IP on the given VNIC.
	CreateSecondaryPrivateIP(ctx context.Context, vnicID string) (ocid, ipAddr string, err error)
	// DeletePrivateIP deletes a secondary private IP by OCID.
	DeletePrivateIP(ctx context.Context, privateIPOCID string) error
	// CreateReservedPublicIP creates a reserved public IP attached to the given private IP.
	CreateReservedPublicIP(ctx context.Context, privateIPOCID, displayName string) (ocid, ipAddr string, err error)
	// ReassignPublicIP moves a reserved public IP to a different private IP.
	ReassignPublicIP(ctx context.Context, publicIPOCID, newPrivateIPOCID string) error
	// GetPublicIPAddress returns the current IP address string for a public IP OCID.
	GetPublicIPAddress(ctx context.Context, publicIPOCID string) (string, error)
	// DeletePublicIP deletes a reserved public IP by OCID.
	DeletePublicIP(ctx context.Context, publicIPOCID string) error
}

// compile-time check that *Client satisfies Interface.
var _ Interface = &Client{}

// Client wraps the OCI VirtualNetwork and Compute clients.
type Client struct {
	vnet          core.VirtualNetworkClient
	compute       core.ComputeClient
	compartmentID string
}

// NewInstancePrincipalClient creates a Client that authenticates using the
// node's OCI instance principal (no API key required).
// compartmentID is the OCI compartment OCID used when creating resources.
func NewInstancePrincipalClient(compartmentID string) (*Client, error) {
	provider, err := auth.InstancePrincipalConfigurationProvider()
	if err != nil {
		return nil, fmt.Errorf("instance principal provider: %w", err)
	}

	vnet, err := core.NewVirtualNetworkClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("vnet client: %w", err)
	}

	compute, err := core.NewComputeClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("compute client: %w", err)
	}

	return &Client{
		vnet:          vnet,
		compute:       compute,
		compartmentID: compartmentID,
	}, nil
}

// PrimaryVNICForInstance returns the OCID of the primary (first attached) VNIC
// for an OCI compute instance.
func (c *Client) PrimaryVNICForInstance(ctx context.Context, instanceOCID string) (string, error) {
	resp, err := c.compute.ListVnicAttachments(ctx, core.ListVnicAttachmentsRequest{
		CompartmentId: common.String(c.compartmentID),
		InstanceId:    common.String(instanceOCID),
	})
	if err != nil {
		return "", fmt.Errorf("list vnic attachments for %s: %w", instanceOCID, err)
	}

	for _, att := range resp.Items {
		if att.LifecycleState == core.VnicAttachmentLifecycleStateAttached {
			return *att.VnicId, nil
		}
	}

	return "", fmt.Errorf("no attached VNIC found for instance %s", instanceOCID)
}

// CreateSecondaryPrivateIP creates a secondary private IP on vnicID.
// OCI assigns an address from the subnet CIDR automatically.
// Returns the private IP OCID and its assigned IP address.
func (c *Client) CreateSecondaryPrivateIP(ctx context.Context, vnicID string) (ocid, ipAddr string, err error) {
	resp, err := c.vnet.CreatePrivateIp(ctx, core.CreatePrivateIpRequest{
		CreatePrivateIpDetails: core.CreatePrivateIpDetails{
			VnicId: common.String(vnicID),
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("create private ip on vnic %s: %w", vnicID, err)
	}

	return *resp.Id, *resp.IpAddress, nil
}

// DeletePrivateIP deletes a secondary private IP by OCID.
func (c *Client) DeletePrivateIP(ctx context.Context, privateIPOCID string) error {
	_, err := c.vnet.DeletePrivateIp(ctx, core.DeletePrivateIpRequest{
		PrivateIpId: common.String(privateIPOCID),
	})
	if err != nil {
		return fmt.Errorf("delete private ip %s: %w", privateIPOCID, err)
	}
	return nil
}

// CreateReservedPublicIP creates a RESERVED public IP and attaches it to privateIPOCID.
// displayName is shown in the OCI console. Returns the public IP OCID and address.
func (c *Client) CreateReservedPublicIP(ctx context.Context, privateIPOCID, displayName string) (ocid, ipAddr string, err error) {
	resp, err := c.vnet.CreatePublicIp(ctx, core.CreatePublicIpRequest{
		CreatePublicIpDetails: core.CreatePublicIpDetails{
			CompartmentId: common.String(c.compartmentID),
			Lifetime:      core.CreatePublicIpDetailsLifetimeReserved,
			DisplayName:   common.String(displayName),
			PrivateIpId:   common.String(privateIPOCID),
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("create reserved public ip: %w", err)
	}

	return *resp.Id, *resp.IpAddress, nil
}

// ReassignPublicIP moves a reserved public IP to a different secondary private IP.
// Used when the elected node changes — the public IP stays the same, only the
// private IP (and thus the VNIC/node) changes.
func (c *Client) ReassignPublicIP(ctx context.Context, publicIPOCID, newPrivateIPOCID string) error {
	_, err := c.vnet.UpdatePublicIp(ctx, core.UpdatePublicIpRequest{
		PublicIpId: common.String(publicIPOCID),
		UpdatePublicIpDetails: core.UpdatePublicIpDetails{
			PrivateIpId: common.String(newPrivateIPOCID),
		},
	})
	if err != nil {
		return fmt.Errorf("reassign public ip %s to private ip %s: %w", publicIPOCID, newPrivateIPOCID, err)
	}
	return nil
}

// GetPublicIPAddress returns the current IP address string for a reserved public IP OCID.
func (c *Client) GetPublicIPAddress(ctx context.Context, publicIPOCID string) (string, error) {
	resp, err := c.vnet.GetPublicIp(ctx, core.GetPublicIpRequest{
		PublicIpId: common.String(publicIPOCID),
	})
	if err != nil {
		return "", fmt.Errorf("get public ip %s: %w", publicIPOCID, err)
	}
	if resp.IpAddress == nil {
		return "", fmt.Errorf("public ip %s has no address yet (still provisioning)", publicIPOCID)
	}
	return *resp.IpAddress, nil
}

// DeletePublicIP deletes a reserved public IP by OCID.
func (c *Client) DeletePublicIP(ctx context.Context, publicIPOCID string) error {
	_, err := c.vnet.DeletePublicIp(ctx, core.DeletePublicIpRequest{
		PublicIpId: common.String(publicIPOCID),
	})
	if err != nil {
		return fmt.Errorf("delete public ip %s: %w", publicIPOCID, err)
	}
	return nil
}

// InstanceOCIDFromProviderID extracts an OCI instance OCID from a Kubernetes
// node's spec.providerID. OCI CCM sets it as "oci://<ocid>" or just "<ocid>".
func InstanceOCIDFromProviderID(providerID string) string {
	return strings.TrimPrefix(providerID, "oci://")
}
