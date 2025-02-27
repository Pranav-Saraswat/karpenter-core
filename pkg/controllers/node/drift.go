/*
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

package node

import (
	"context"
	"fmt"
	"time"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/settings"
	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/utils/machine"
)

type Drift struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
}

func (d *Drift) Reconcile(ctx context.Context, provisioner *v1alpha5.Provisioner, node *v1.Node) (reconcile.Result, error) {
	// node is not ready yet, so we don't consider it to be drifted
	if node.Labels[v1alpha5.LabelNodeInitialized] != "true" {
		return reconcile.Result{}, nil
	}

	// If the node is marked as voluntarily disrupted by another controller, do nothing.
	val, hasAnnotation := node.Annotations[v1alpha5.VoluntaryDisruptionAnnotationKey]
	if hasAnnotation && val != v1alpha5.VoluntaryDisruptionDriftedAnnotationValue {
		return reconcile.Result{}, nil
	}

	// From here there are three scenarios to handle:
	// 1. If drift is not enabled but the node is drifted, remove the annotation
	//    so another disruption controller can annotate the node.
	if !settings.FromContext(ctx).DriftEnabled {
		if val == v1alpha5.VoluntaryDisruptionDriftedAnnotationValue {
			delete(node.Annotations, v1alpha5.VoluntaryDisruptionAnnotationKey)
			logging.FromContext(ctx).Debugf("removing drift annotation from node as drift has been disabled")
		}
		return reconcile.Result{}, nil
	}
	drifted, err := d.cloudProvider.IsMachineDrifted(ctx, machine.NewFromNode(node))
	if err != nil {
		return reconcile.Result{}, cloudprovider.IgnoreMachineNotFoundError(fmt.Errorf("getting drift for node, %w", err))
	}
	// 2. Otherwise, if the node isn't drifted, but has the annotation, remove it.
	if !drifted && hasAnnotation {
		delete(node.Annotations, v1alpha5.VoluntaryDisruptionAnnotationKey)
		logging.FromContext(ctx).Debugf("removing drift annotation from node")
		// 3. Finally, if the node is drifted, but doesn't have the annotation, add it.
	} else if drifted && !hasAnnotation {
		node.Annotations = lo.Assign(node.Annotations, map[string]string{
			v1alpha5.VoluntaryDisruptionAnnotationKey: v1alpha5.VoluntaryDisruptionDriftedAnnotationValue,
		})
		logging.FromContext(ctx).Debugf("annotating node as drifted")
	}

	// Requeue after 5 minutes for the cache TTL
	return reconcile.Result{RequeueAfter: 5 * time.Minute}, nil
}
