/** Stop only between requests: an in-flight delete must settle before reporting. */
export async function deleteSequentially<T>(
  items: readonly T[],
  shouldStop: () => boolean,
  remove: (item: T) => Promise<void>,
  onResult: (item: T, error: unknown | null) => void,
) {
  let done = 0
  for (const item of items) {
    if (shouldStop()) break
    try {
      await remove(item)
      onResult(item, null)
    } catch (error) {
      onResult(item, error ?? new Error('Delete failed'))
    }
    done++
  }
  return { done, remaining: items.length - done }
}
