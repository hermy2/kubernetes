/*
Copyright 2019 The Kubernetes Authors.

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

package filters

import (
	"context"
	"fmt"
	"net/http"

	// TODO: decide whether to also generate the old metrics, which
	// categorize according to mutating vs readonly.

	// "k8s.io/apiserver/pkg/endpoints/metrics"

	fcv1a1 "k8s.io/api/flowcontrol/v1alpha1"
	apitypes "k8s.io/apimachinery/pkg/types"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	utilflowcontrol "k8s.io/apiserver/pkg/util/flowcontrol"
	"k8s.io/klog"
)

type priorityAndFairnessKeyType int

const priorityAndFairnessKey priorityAndFairnessKeyType = iota

// PriorityAndFairnessClassification identifies the results of
// classification for API Priority and Fairness
type PriorityAndFairnessClassification struct {
	FlowSchemaName    string
	FlowSchemaUID     apitypes.UID
	PriorityLevelName string
	PriorityLevelUID  apitypes.UID
}

// GetClassification returns the classification associated with the
// given context, if any, otherwise nil
func GetClassification(ctx context.Context) *PriorityAndFairnessClassification {
	return ctx.Value(priorityAndFairnessKey).(*PriorityAndFairnessClassification)
}

// WithPriorityAndFairness limits the number of in-flight
// requests in a fine-grained way.
func WithPriorityAndFairness(
	handler http.Handler,
	longRunningRequestCheck apirequest.LongRunningRequestCheck,
	fcIfc utilflowcontrol.Interface,
) http.Handler {
	if fcIfc == nil {
		klog.Warningf("priority and fairness support not found, skipping")
		return handler
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		requestInfo, ok := apirequest.RequestInfoFrom(ctx)
		if !ok {
			handleError(w, r, fmt.Errorf("no RequestInfo found in context"))
			return
		}
		user, ok := apirequest.UserFrom(ctx)
		if !ok {
			handleError(w, r, fmt.Errorf("no User found in context"))
			return
		}

		// Skip tracking long running requests.
		if longRunningRequestCheck != nil && longRunningRequestCheck(r, requestInfo) {
			klog.V(6).Infof("Serving RequestInfo=%#+v, user.Info=%#+v as longrunning\n", requestInfo, user)
			handler.ServeHTTP(w, r)
			return
		}

		var classification *PriorityAndFairnessClassification
		note := func(fs *fcv1a1.FlowSchema, pl *fcv1a1.PriorityLevelConfiguration) {
			classification = &PriorityAndFairnessClassification{
				FlowSchemaName:    fs.Name,
				FlowSchemaUID:     fs.UID,
				PriorityLevelName: pl.Name,
				PriorityLevelUID:  pl.UID}
		}
		var served bool
		execute := func() {
			served = true
			innerCtx := context.WithValue(ctx, priorityAndFairnessKey, classification)
			innerReq := r.Clone(innerCtx)
			handler.ServeHTTP(w, innerReq)
		}
		digest := utilflowcontrol.RequestDigest{requestInfo, user}
		fcIfc.Handle(ctx, digest, note, execute)
		if !served {
			tooManyRequests(r, w)
			return
		}

	})
}
