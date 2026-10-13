package runtime

// This file has been modified for the KestrelDesk project.

import (
	"encoding/json"

	"github.com/Swhshd/kestreldesk/internal/ai/workflow/dsl"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/repositories"

	"github.com/mlogclub/simple/sqls"
)

type resolvedWorkflow struct {
	Definition dsl.Definition
	WorkflowID int64
	VersionID  int64
}

func resolveWorkflowVersion(workflowVersionID int64) (resolvedWorkflow, error) {
	if workflowVersionID <= 0 {
		return resolvedWorkflow{}, errorsx.InvalidParam("workflow version is required")
	}
	version := repositories.AIWorkflowVersionRepository.Get(sqls.DB(), workflowVersionID)
	if version == nil || version.Status != enums.StatusOk {
		return resolvedWorkflow{}, errorsx.InvalidParam("workflow version does not exist")
	}
	var def dsl.Definition
	if err := json.Unmarshal([]byte(version.Definition), &def); err != nil {
		return resolvedWorkflow{}, errorsx.InvalidParam("workflow definition is invalid")
	}
	return resolvedWorkflow{
		Definition: def,
		WorkflowID: version.WorkflowID,
		VersionID:  version.ID,
	}, nil
}
