// Messages created on the Octo channel use its authorization-scoped artifact API.
// Web continuations in the same session retain the regular Web artifact API.
export function isChannelArtifactMessage(chatMode, platform, messageChannel) {
  return chatMode === 'channel' || (platform === 'octo' && messageChannel === 'im')
}
