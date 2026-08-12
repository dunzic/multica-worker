package rolesource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
)

var ErrArtifactUnavailable = errors.New("role source adapter does not provide artifact bodies")

// OpenArtifact invokes the optional daemon-only artifact capability after the
// same adapter/config/reference validation used by scan. It never runs in the
// server control plane.
func (r *Registry) OpenArtifact(ctx context.Context, kind Kind, request ScanRequest, ref ArtifactRef) (io.ReadCloser, error) {
	r.mu.RLock()
	registered, ok := r.adapters[kind]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrAdapterNotFound, kind)
	}
	if err := registered.adapter.ValidateConfig(request.Config); err != nil {
		return nil, fmt.Errorf("validate %s config: %w", kind, err)
	}
	if err := validateArtifact(ref); err != nil {
		return nil, err
	}
	opener, ok := registered.adapter.(ArtifactOpener)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrArtifactUnavailable, kind)
	}
	return opener.OpenArtifact(ctx, request, ref)
}

// CollectArtifactRefs returns one canonical reference per content digest.
// Reusing a digest with a different size is rejected because the server's
// content ledger cannot safely satisfy both declarations.
func CollectArtifactRefs(snapshot Snapshot) ([]ArtifactRef, error) {
	canonical, err := validatedSnapshotCopy(snapshot)
	if err != nil {
		return nil, err
	}
	byDigest := make(map[string]ArtifactRef)
	add := func(ref ArtifactRef) error {
		existing, ok := byDigest[ref.Digest]
		if ok {
			if existing.SizeBytes != ref.SizeBytes {
				return fmt.Errorf("artifact digest %q has conflicting sizes", ref.Digest)
			}
			if ref.Path < existing.Path {
				byDigest[ref.Digest] = ref
			}
			return nil
		}
		byDigest[ref.Digest] = ref
		return nil
	}
	for _, capability := range canonical.Manifest.Capabilities {
		if err := add(capability.Entrypoint); err != nil {
			return nil, err
		}
		for _, ref := range capability.Artifacts {
			if err := add(ref); err != nil {
				return nil, err
			}
		}
	}
	for _, role := range canonical.Manifest.Roles {
		if err := add(role.Instructions); err != nil {
			return nil, err
		}
		if role.Profile != nil {
			if err := add(*role.Profile); err != nil {
				return nil, err
			}
		}
		for _, skill := range role.Skills {
			if err := add(skill.Entrypoint); err != nil {
				return nil, err
			}
			for _, ref := range skill.Artifacts {
				if err := add(ref); err != nil {
					return nil, err
				}
			}
		}
		for _, automation := range role.Automations {
			if err := add(automation.Prompt); err != nil {
				return nil, err
			}
		}
	}
	refs := make([]ArtifactRef, 0, len(byDigest))
	for _, ref := range byDigest {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Digest < refs[j].Digest })
	return refs, nil
}

// collectMaterializationArtifactRefs returns only bodies copied into Multica's
// mutable execution tables during apply. The complete snapshot artifact set is
// still transferred and retained by digest, but capability packages and
// supporting files and only bound capability packages are copied into target
// skills. Unbound catalog packages remain content-addressed in object storage;
// loading them would make a large catalog an unnecessary server-memory event.
func collectMaterializationArtifactRefs(snapshot Snapshot) ([]ArtifactRef, error) {
	canonical, err := validatedSnapshotCopy(snapshot)
	if err != nil {
		return nil, err
	}
	byDigest := make(map[string]ArtifactRef)
	add := func(ref ArtifactRef) error {
		existing, ok := byDigest[ref.Digest]
		if ok {
			if existing.SizeBytes != ref.SizeBytes {
				return fmt.Errorf("artifact digest %q has conflicting sizes", ref.Digest)
			}
			if ref.Path < existing.Path {
				byDigest[ref.Digest] = ref
			}
			return nil
		}
		byDigest[ref.Digest] = ref
		return nil
	}
	for _, role := range canonical.Manifest.Roles {
		if err := add(role.Instructions); err != nil {
			return nil, err
		}
		for _, skill := range role.Skills {
			if err := add(skill.Entrypoint); err != nil {
				return nil, err
			}
			for _, ref := range skill.Artifacts {
				if err := add(ref); err != nil {
					return nil, err
				}
			}
		}
		for _, automation := range role.Automations {
			if err := add(automation.Prompt); err != nil {
				return nil, err
			}
		}
	}
	capabilities := make(map[string]Capability, len(canonical.Manifest.Capabilities))
	for _, capability := range canonical.Manifest.Capabilities {
		capabilities[capability.ID] = capability
	}
	bound := make(map[string]bool)
	for _, role := range canonical.Manifest.Roles {
		for _, binding := range role.CapabilityBindings {
			bound[binding.CapabilityID] = true
		}
	}
	for id := range bound {
		capability := capabilities[id]
		if err := add(capability.Entrypoint); err != nil {
			return nil, err
		}
		for _, ref := range capability.Artifacts {
			if err := add(ref); err != nil {
				return nil, err
			}
		}
	}
	refs := make([]ArtifactRef, 0, len(byDigest))
	for _, ref := range byDigest {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Digest < refs[j].Digest })
	return refs, nil
}
