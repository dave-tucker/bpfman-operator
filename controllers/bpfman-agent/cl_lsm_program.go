/*
Copyright 2025 The bpfman Authors.

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

package bpfmanagent

import (
	"context"
	"fmt"

	bpfmaniov1alpha1 "github.com/bpfman/bpfman-operator/apis/v1alpha1"
	internal "github.com/bpfman/bpfman-operator/internal"
	gobpfman "github.com/bpfman/bpfman/clients/gobpfman/v1"
	"github.com/google/uuid"
)

// ClLsmProgramReconciler contains the info required to reconcile an
// LsmProgram
type ClLsmProgramReconciler struct {
	ReconcilerCommon
	ClProgramReconcilerCommon
	currentLink *bpfmaniov1alpha1.ClLsmAttachInfoState
}

func (r *ClLsmProgramReconciler) getProgId() *uint32 {
	return r.currentProgramState.ProgramId
}

func (r *ClLsmProgramReconciler) getProgType() internal.ProgramType {
	return internal.Lsm
}

func (r *ClLsmProgramReconciler) getBpfmanProgType() gobpfman.BpfmanProgramType {
	return gobpfman.BpfmanProgramType_LSM
}

func (r *ClLsmProgramReconciler) getProgName() string {
	return r.currentProgram.Name
}

func (r *ClLsmProgramReconciler) shouldAttach() bool {
	return r.currentLink.ShouldAttach
}

func (r *ClLsmProgramReconciler) isAttached(ctx context.Context) bool {
	if r.currentProgramState.ProgramId == nil || r.currentLink.LinkId == nil {
		return false
	}
	return r.doesLinkExist(ctx, *r.currentProgramState.ProgramId, *r.currentLink.LinkId)
}

func (r *ClLsmProgramReconciler) getUUID() string {
	return r.currentLink.UUID
}

func (r *ClLsmProgramReconciler) getLinkId() *uint32 {
	return r.currentLink.LinkId
}

func (r *ClLsmProgramReconciler) setLinkId(id *uint32) {
	r.currentLink.LinkId = id
}

func (r *ClLsmProgramReconciler) setProgramLinkStatus(status bpfmaniov1alpha1.ProgramLinkStatus) {
	r.currentProgramState.ProgramLinkStatus = status
}

func (r *ClLsmProgramReconciler) getProgramLinkStatus() bpfmaniov1alpha1.ProgramLinkStatus {
	return r.currentProgramState.ProgramLinkStatus
}

func (r *ClLsmProgramReconciler) setCurrentLinkStatus(status bpfmaniov1alpha1.LinkStatus) {
	r.currentLink.LinkStatus = status
}

func (r *ClLsmProgramReconciler) getCurrentLinkStatus() bpfmaniov1alpha1.LinkStatus {
	return r.currentLink.LinkStatus
}

func (r *ClLsmProgramReconciler) getAttachRequest() *gobpfman.AttachRequest {
	return &gobpfman.AttachRequest{
		Id: *r.currentProgramState.ProgramId,
		Attach: &gobpfman.AttachInfo{
			Info: &gobpfman.AttachInfo_LsmAttachInfo{
				LsmAttachInfo: &gobpfman.LsmAttachInfo{
					Metadata: map[string]string{internal.UuidMetadataKey: string(r.currentLink.UUID)},
				},
			},
		},
	}
}

// updateLinks processes the *ProgramInfo and updates the list of links
// contained in *AttachInfoState.
func (r *ClLsmProgramReconciler) updateLinks(ctx context.Context, isBeingDeleted bool) error {
	r.Logger.Info("Lsm updateAttachInfo()", "isBeingDeleted", isBeingDeleted)
	// Set ShouldAttach for all links in the node CRD to false.  We'll
	// update this in the next step for all links that are still
	// present.

	for i := range r.currentProgramState.Lsm.Links {
		r.currentProgramState.Lsm.Links[i].ShouldAttach = false
	}

	if isBeingDeleted {
		// If the program is being deleted, we don't need to do anything else.
		return nil
	}

	if r.currentProgram.Lsm != nil && r.currentProgram.Lsm.Links != nil {
		for _, attachInfo := range r.currentProgram.Lsm.Links {
			expectedLinks, error := r.getExpectedLinks(attachInfo)
			if error != nil {
				return fmt.Errorf("failed to get node links: %v", error)
			}
			for _, link := range expectedLinks {
				index := r.findLink(link)
				if index != nil {
					// Link already exists, so set ShouldAttach to true.
					r.currentProgramState.Lsm.Links[*index].AttachInfoStateCommon.ShouldAttach = true
				} else {
					// Link doesn't exist, so add it.
					r.Logger.Info("Link doesn't exist.  Adding it.")
					r.currentProgramState.Lsm.Links = append(r.currentProgramState.Lsm.Links, link)
				}
			}
		}
	}

	// If any existing link is no longer on a list of expected links
	// ShouldAttach will remain set to false and it will get detached in a
	// following step.

	return nil
}

func (r *ClLsmProgramReconciler) findLink(_ bpfmaniov1alpha1.ClLsmAttachInfoState) *int {
	for i := range r.currentProgramState.Lsm.Links {
		return &i
	}
	return nil
}

// processLinks calls reconcileBpfLink() for each link. It
// then updates the ProgramAttachStatus based on the updated status of each
// link.
func (r *ClLsmProgramReconciler) processLinks(ctx context.Context) error {
	r.Logger.Info("Processing attach info", "bpfFunctionName", r.currentProgram.Name)

	// The following map is used to keep track of links that need to be
	// removed.  If it's not empty at the end of the loop, we'll remove the
	// links.
	linksToRemove := make(map[int]bool)

	var lastReconcileLinkError error = nil
	for i := range r.currentProgramState.Lsm.Links {
		r.currentLink = &r.currentProgramState.Lsm.Links[i]
		remove, err := r.reconcileBpfLink(ctx, r)
		if err != nil {
			r.Logger.Error(err, "failed to reconcile bpf link", "index", i)
			// All errors are logged, but the last error is saved to return and
			// we continue to process the rest of the links so errors
			// don't block valid links.
			lastReconcileLinkError = err
		}

		if remove {
			r.Logger.Info("Marking link for removal", "index", i)
			linksToRemove[i] = true
		}
	}

	if len(linksToRemove) > 0 {
		r.Logger.Info("Removing links", "linksToRemove", linksToRemove)
		r.currentProgramState.Lsm.Links = r.removeLinks(r.currentProgramState.Lsm.Links, linksToRemove)
	}

	r.updateProgramAttachStatus()

	return lastReconcileLinkError
}

func (r *ClLsmProgramReconciler) updateProgramAttachStatus() {
	for _, link := range r.currentProgramState.Lsm.Links {
		if !isAttachSuccess(link.ShouldAttach, link.LinkStatus) {
			r.setProgramLinkStatus(bpfmaniov1alpha1.ProgAttachError)
			return
		}
	}
	r.setProgramLinkStatus(bpfmaniov1alpha1.ProgAttachSuccess)
}

// removeLinks removes links from a slice of links based on the keys in the map.
func (r *ClLsmProgramReconciler) removeLinks(links []bpfmaniov1alpha1.ClLsmAttachInfoState, linksToRemove map[int]bool) []bpfmaniov1alpha1.ClLsmAttachInfoState {
	var remainingLinks []bpfmaniov1alpha1.ClLsmAttachInfoState
	for i, a := range links {
		if _, ok := linksToRemove[i]; !ok {
			remainingLinks = append(remainingLinks, a)
		}
	}
	return remainingLinks
}

// getExpectedLinks expands *AttachInfo into a list of specific attach
// points.
func (r *ClLsmProgramReconciler) getExpectedLinks(_ bpfmaniov1alpha1.ClLsmAttachInfo,
) ([]bpfmaniov1alpha1.ClLsmAttachInfoState, error) {
	nodeLinks := []bpfmaniov1alpha1.ClLsmAttachInfoState{}

	link := bpfmaniov1alpha1.ClLsmAttachInfoState{
		AttachInfoStateCommon: bpfmaniov1alpha1.AttachInfoStateCommon{
			ShouldAttach: true,
			UUID:         uuid.New().String(),
			LinkId:       nil,
			LinkStatus:   bpfmaniov1alpha1.ApAttachNotAttached,
		},
	}
	nodeLinks = append(nodeLinks, link)

	return nodeLinks, nil
}

func (r *ClLsmProgramReconciler) getProgramLoadInfo() *gobpfman.LoadInfo {
	return &gobpfman.LoadInfo{
		Name:        r.currentProgram.Name,
		ProgramType: r.getBpfmanProgType(),
		Info: &gobpfman.ProgSpecificInfo{
			Info: &gobpfman.ProgSpecificInfo_LsmLoadInfo{
				LsmLoadInfo: &gobpfman.LsmLoadInfo{
					HookName: r.currentProgram.Lsm.Hook,
				},
			},
		},
	}
}
