import type { ArtifactUsageResponse } from '@/api/artifacts'

// Do not turn an unavailable, partial, or differently scoped response into zero.
export function parseArtifactUsage(response: ArtifactUsageResponse | null | undefined): ArtifactUsageResponse['data'] | null {
  const data = response?.data
  if (response?.success !== true || data?.scope !== 'accessible_artifact_versions' ||
      !Number.isSafeInteger(data.file_count) || data.file_count < 0 ||
      !Number.isSafeInteger(data.logical_bytes) || data.logical_bytes < 0) return null
  return data
}
