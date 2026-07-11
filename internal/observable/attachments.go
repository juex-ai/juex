package observable

import "github.com/juex-ai/juex/internal/eventmedia"

type attachmentSnapshot struct {
	refs               []eventmedia.AttachmentRef
	errors             []string
	bytes              int64
	eventBytesExceeded bool
}

func snapshotAttachmentRefs(workDir string, refs []eventmedia.AttachmentRef, maxEventBytes int64) attachmentSnapshot {
	if len(refs) == 0 {
		return attachmentSnapshot{}
	}
	report := eventmedia.ValidateAttachments(refs, eventmedia.ValidationOptions{
		WorkDir:       workDir,
		MaxEventBytes: maxEventBytes,
	})
	stored := make([]eventmedia.AttachmentRef, 0, len(report.Valid))
	var storedBytes int64
	for _, attachment := range report.Valid {
		stored = append(stored, eventmedia.AttachmentRef{
			Path:      attachment.ArtifactPath,
			MediaType: attachment.MediaType,
		})
		storedBytes += int64(attachment.OriginalBytes)
	}
	errors := make([]string, 0, len(report.Errors))
	for _, errInfo := range report.Errors {
		if errInfo.Path != "" {
			errors = append(errors, errInfo.Path+": "+errInfo.Error)
		} else {
			errors = append(errors, errInfo.Error)
		}
	}
	return attachmentSnapshot{
		refs:               stored,
		errors:             errors,
		bytes:              storedBytes,
		eventBytesExceeded: report.EventBytesExceeded,
	}
}
