type TFunction = (key: string, values?: Record<string, string | number>) => string

const CHANNEL_LABEL_KEYS: Record<string, string> = {
  im: "knowledge.channelIM",
  agent_assist: "knowledge.channelAgentAssist",
  api: "knowledge.channelAPI",
  debug: "knowledge.channelDebug",
}

const SCENE_LABEL_KEYS: Record<string, string> = {
  first_response: "knowledge.sceneFirstResponse",
  assist: "knowledge.sceneAssist",
  qa: "knowledge.sceneQA",
}

const ANSWER_STATUS_LABEL_KEYS: Record<number, string> = {
  1: "knowledge.answerNormal",
  2: "knowledge.answerNoAnswer",
  3: "knowledge.answerFallback",
  4: "knowledge.answerBlocked",
}

const PROVIDER_LABEL_KEYS: Record<string, string> = {
  fixed: "knowledge.chunkFixed",
  structured: "knowledge.chunkStructured",
  faq: "knowledge.chunkFAQ",
  semantic: "knowledge.chunkSemantic",
}

export function getKnowledgeRetrieveChannelLabel(value: string, fallback: string, t: TFunction) {
  return translateByKey(CHANNEL_LABEL_KEYS[value], fallback, t)
}

export function getKnowledgeRetrieveSceneLabel(value: string, fallback: string, t: TFunction) {
  return translateByKey(SCENE_LABEL_KEYS[value], fallback, t)
}

export function getKnowledgeAnswerStatusLabel(value: number, fallback: string, t: TFunction) {
  return translateByKey(ANSWER_STATUS_LABEL_KEYS[value], fallback, t)
}

export function getKnowledgeChunkProviderLabel(value: string, t: TFunction) {
  return translateByKey(PROVIDER_LABEL_KEYS[value], value, t)
}

function translateByKey(key: string | undefined, fallback: string, t: TFunction) {
  return key ? t(key) : fallback
}
