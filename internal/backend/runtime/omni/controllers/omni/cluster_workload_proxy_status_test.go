// Copyright (c) 2025 Sidero Labs, Inc.
//
// Use of this software is governed by the Business Source License
// included in the LICENSE file.

package omni_test

import (
	"context"
	_ "embed"
	"sync"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/rtestutils"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/omni/resources"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	omnictrl "github.com/siderolabs/omni/internal/backend/runtime/omni/controllers/omni"
)

type ClusterWorkloadProxyStatusSuite struct {
	OmniSuite
}

func (suite *ClusterWorkloadProxyStatusSuite) TestReconcile() {
	suite.startRuntime()

	ctx, cancel := context.WithTimeout(suite.ctx, time.Second*5)
	defer cancel()

	workloadProxyReconciler := &mockWorkloadProxyReconciler{}

	suite.Require().NoError(suite.runtime.RegisterQController(omnictrl.NewClusterWorkloadProxyStatusController(workloadProxyReconciler)))

	clusterID := "test-cluster-1"
	cluster := omni.NewCluster(resources.DefaultNamespace, clusterID)
	cluster.TypedSpec().Value.Features = &specs.ClusterSpec_Features{
		EnableWorkloadProxy: true,
	}

	suite.Require().NoError(suite.state.Create(ctx, cluster))

	workloadProxyReconciler.assertState(suite.T(), nil)

	// create an exposed service
	suite.createExposedService(clusterID, "test-exposed-service-1", 12345)

	// create a healthy upstream cluster machine status
	suite.createClusterMachineStatus(clusterID, "test-cms-1")

	workloadProxyReconciler.assertState(suite.T(), map[resource.ID]map[string][]string{
		clusterID: {
			"test-exposed-service-1-alias": {"test-cms-1-management-address:12345"},
		},
	})

	// add another healthy upstream cluster machine status
	suite.createClusterMachineStatus(clusterID, "test-cms-2")

	workloadProxyReconciler.assertState(suite.T(), map[resource.ID]map[string][]string{
		clusterID: {
			"test-exposed-service-1-alias": {"test-cms-1-management-address:12345", "test-cms-2-management-address:12345"},
		},
	})

	// add another healthy upstream cluster machine status
	suite.createClusterMachineStatus(clusterID, "test-cms-3")

	rtestutils.AssertResources[*omni.ClusterMachineStatus](ctx, suite.T(), suite.state, []string{"test-cms-3"}, func(r *omni.ClusterMachineStatus, assertion *assert.Assertions) {
		assertion.True(r.Metadata().Finalizers().Has(omnictrl.ClusterWorkloadProxyStatusControllerName))
	})

	// turn off the feature for the cluster
	_, err := safe.StateUpdateWithConflicts(ctx, suite.state, cluster.Metadata(), func(cluster *omni.Cluster) error {
		cluster.TypedSpec().Value.Features.EnableWorkloadProxy = false

		return nil
	})
	suite.Require().NoError(err)

	workloadProxyReconciler.assertState(suite.T(), nil)

	// delete one of the machines
	rtestutils.Destroy[*omni.ClusterMachineStatus](ctx, suite.T(), suite.state, []string{"test-cms-3"})

	// turn it back on
	_, err = safe.StateUpdateWithConflicts(ctx, suite.state, cluster.Metadata(), func(cluster *omni.Cluster) error {
		cluster.TypedSpec().Value.Features.EnableWorkloadProxy = true

		return nil
	})
	suite.Require().NoError(err)

	workloadProxyReconciler.assertState(suite.T(), map[resource.ID]map[string][]string{
		clusterID: {
			"test-exposed-service-1-alias": {"test-cms-1-management-address:12345", "test-cms-2-management-address:12345"},
		},
	})

	// destroy cluster
	rtestutils.Destroy[*omni.Cluster](ctx, suite.T(), suite.state, []string{clusterID})

	workloadProxyReconciler.assertState(suite.T(), nil)
}

func (suite *ClusterWorkloadProxyStatusSuite) TestReconcileMappedInputDeletion() {
	suite.startRuntime()

	ctx, cancel := context.WithTimeout(suite.ctx, time.Second*10)
	defer cancel()

	workloadProxyReconciler := &mockWorkloadProxyReconciler{}

	suite.Require().NoError(suite.runtime.RegisterQController(omnictrl.NewClusterWorkloadProxyStatusController(workloadProxyReconciler)))

	clusterID := "test-cluster"
	cluster := omni.NewCluster(resources.DefaultNamespace, clusterID)
	cluster.TypedSpec().Value.Features = &specs.ClusterSpec_Features{
		EnableWorkloadProxy: true,
	}

	suite.Require().NoError(suite.state.Create(ctx, cluster))

	workloadProxyReconciler.assertState(suite.T(), nil)

	suite.createExposedService(clusterID, "test-exposed-service-1", 12345)
	suite.createClusterMachineStatus(clusterID, "test-cms-1")

	es2 := suite.createExposedService(clusterID, "test-exposed-service-2", 23456)
	cms2 := suite.createClusterMachineStatus(clusterID, "test-cms-2")

	workloadProxyReconciler.assertState(suite.T(), map[resource.ID]map[string][]string{
		clusterID: {
			"test-exposed-service-1-alias": {"test-cms-1-management-address:12345", "test-cms-2-management-address:12345"},
			"test-exposed-service-2-alias": {"test-cms-1-management-address:23456", "test-cms-2-management-address:23456"},
		},
	})

	rtestutils.Destroy[*omni.ExposedService](ctx, suite.T(), suite.state, []string{es2.Metadata().ID()})

	workloadProxyReconciler.assertState(suite.T(), map[resource.ID]map[string][]string{
		clusterID: {
			"test-exposed-service-1-alias": {"test-cms-1-management-address:12345", "test-cms-2-management-address:12345"},
		},
	})

	rtestutils.Destroy[*omni.ClusterMachineStatus](ctx, suite.T(), suite.state, []string{cms2.Metadata().ID()})

	workloadProxyReconciler.assertState(suite.T(), map[resource.ID]map[string][]string{
		clusterID: {
			"test-exposed-service-1-alias": {"test-cms-1-management-address:12345"},
		},
	})
}

func (suite *ClusterWorkloadProxyStatusSuite) createClusterMachineStatus(clusterID string, id resource.ID) *omni.ClusterMachineStatus {
	suite.T().Helper()

	cms := omni.NewClusterMachineStatus(resources.DefaultNamespace, id)

	cms.Metadata().Labels().Set(omni.LabelCluster, clusterID)

	cms.TypedSpec().Value.Ready = true
	cms.TypedSpec().Value.ManagementAddress = id + "-management-address"

	suite.Require().NoError(suite.state.Create(suite.ctx, cms))

	return cms
}

func (suite *ClusterWorkloadProxyStatusSuite) createExposedService(clusterID string, id resource.ID, port uint32) *omni.ExposedService {
	suite.T().Helper()

	es := omni.NewExposedService(resources.DefaultNamespace, id)

	es.Metadata().Labels().Set(omni.LabelCluster, clusterID)
	es.Metadata().Labels().Set(omni.LabelExposedServiceAlias, id+"-alias")

	es.TypedSpec().Value.Port = port

	suite.Require().NoError(suite.state.Create(suite.ctx, es))

	return es
}

func TestClusterWorkloadProxyStatusSuite(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(ClusterWorkloadProxyStatusSuite))
}

type mockWorkloadProxyReconciler struct {
	data map[resource.ID]map[string][]string
	mu   sync.Mutex
}

func (m *mockWorkloadProxyReconciler) Reconcile(cluster resource.ID, aliasToUpstreamAddresses map[string][]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(aliasToUpstreamAddresses) == 0 {
		delete(m.data, cluster)

		if len(m.data) == 0 {
			m.data = nil
		}

		return nil
	}

	if m.data == nil {
		m.data = map[resource.ID]map[string][]string{}
	}

	m.data[cluster] = aliasToUpstreamAddresses

	return nil
}

func (m *mockWorkloadProxyReconciler) assertState(t *testing.T, expected map[resource.ID]map[string][]string) {
	t.Helper()

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		m.mu.Lock()
		defer m.mu.Unlock()

		assert.Equal(collect, expected, m.data)
	}, time.Second*1, time.Millisecond*50)
}
