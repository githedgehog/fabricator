# EMC Fork Strategy

This document describes fork-specific patches and modifications made to support the Extended Virtual Lab (evlab) project and External Management Cluster (EMC) implementation on the `afewell-hh/fabricator` fork.

## Purpose

This fork is maintained for the evlab project and diverges from upstream `githedgehog/fabricator` only where necessary. All changes documented here are isolated and can be dropped or submitted upstream as appropriate.

---

## Fork-Specific Patches

### 1. Helm Chart Version Semver Compliance (Issue #12)

**File:** `hack/tools.just`
**Lines:** 7-13
**Issue:** [#12](https://github.com/afewell-hh/fabricator/issues/12)

#### Problem

CI fails in the `_helm-fabricator-api` recipe during `just --timestamp build kube-build build-multi` because Helm requires semantic versions but the version string from `git describe --tags --dirty --always` can return a raw git SHA (e.g., `64d17628`) when no tag is reachable. Helm's package command rejects this as invalid semver.

#### Solution

Modified version handling logic to ensure semver compliance:

```just
version_raw := `git describe --tags --dirty --always` + version_dirty
version_base := if version_raw =~ '^v?[0-9]+\.[0-9]+\.[0-9]+' { version_raw } else { "0.0.0-" + version_raw }
version := version_base + version_extra
```

**Behavior:**
- If `git describe` returns a version with semver format (e.g., `v0.42.0-216-gec19e921`): use as-is
- If `git describe` returns only a SHA (e.g., `64d17628`): transform to `0.0.0-64d17628`

This ensures Helm packaging always succeeds regardless of repository tag state.

#### Testing

Added `test-version` recipe to validate version format:

```bash
just test-version
```

This recipe validates that the computed version matches Helm's semver requirements.

#### Isolation

This patch is isolated to the version computation logic in `hack/tools.just` and does not affect:
- Upstream version handling for tagged releases
- The actual build artifacts or runtime behavior
- Any component version definitions in `pkg/fab/versions.go`

#### Upstream Consideration

This could be submitted to upstream if they want to support building from arbitrary commits without tags. However, upstream may prefer a different approach (e.g., failing fast when no tag exists).

#### Sync Strategy

When syncing from upstream:
1. Check if upstream has modified version handling in `hack/tools.just`
2. If so, manually reapply this patch or adopt upstream's solution if equivalent
3. Always run `just test-version` after sync to ensure compliance

---

## Sync Workflow

To keep this fork synchronized with upstream while preserving patches:

```bash
# Add upstream remote (one-time setup)
git remote add upstream https://github.com/githedgehog/fabricator.git

# Fetch upstream changes
git fetch upstream master

# Merge upstream into fork's master
git checkout master
git merge upstream/master

# Review conflicts (if any) in files documented above
git status

# Test that patches still apply
just test-version
just --timestamp build kube-build build-multi

# Push synchronized master
git push origin master
```

---

## Future Patches

Additional fork-specific changes will be documented here as they are implemented. Each entry should include:

1. **Problem:** What upstream issue or evlab requirement necessitates the patch
2. **Solution:** The specific code changes made
3. **Testing:** How to verify the patch works correctly
4. **Isolation:** Impact scope and files affected
5. **Upstream Consideration:** Whether/when to submit upstream
6. **Sync Strategy:** Special handling needed during upstream merges

---

## Maintenance Notes

- This fork is NOT intended for long-term divergence from upstream
- All patches should be clearly isolated and documented here
- Prefer contributing fixes upstream when they have general utility
- Fork-specific features (e.g., EMC components) should be in separate namespaces where possible

---

**Last Updated:** 2025-11-08
**Maintainer:** afewell-hh
**Related Issues:** [#1 (EMC Epic)](https://github.com/afewell-hh/fabricator/issues/1), [#12 (Helm Semver)](https://github.com/afewell-hh/fabricator/issues/12)
