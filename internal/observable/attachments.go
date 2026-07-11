package observable

import "github.com/juex-ai/juex/internal/eventmedia"

func snapshotAttachmentRefs(workDir string, refs []eventmedia.AttachmentRef) ([]eventmedia.AttachmentRef, []string) {
	if len(refs) == 0 {
		return nil, nil
	}
	report := eventmedia.ValidateAttachments(refs, eventmedia.ValidationOptions{WorkDir: workDir})
	stored := make([]eventmedia.AttachmentRef, 0, len(report.Valid))
	for _, attachment := range report.Valid {
		stored = append(stored, eventmedia.AttachmentRef{
			Path:      attachment.ArtifactPath,
			MediaType: attachment.MediaType,
		})
	}
	errors := make([]string, 0, len(report.Errors))
	for _, errInfo := range report.Errors {
		if errInfo.Path != "" {
			errors = append(errors, errInfo.Path+": "+errInfo.Error)
		} else {
			errors = append(errors, errInfo.Error)
		}
	}
	return stored, errors
}
