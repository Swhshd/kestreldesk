import assert from "node:assert/strict"
import test from "node:test"
import ts from "typescript"
import { readFile } from "node:fs/promises"
import vm from "node:vm"

async function loadModule() {
  const source = await readFile(new URL("./knowledge-i18n.ts", import.meta.url), "utf8")
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2017,
      module: ts.ModuleKind.CommonJS,
    },
    fileName: "knowledge-i18n.ts",
  })
  const sandbox = {
    exports: {},
    module: { exports: {} },
  }
  sandbox.exports = sandbox.module.exports
  vm.runInNewContext(compiled.outputText, sandbox)
  return sandbox.module.exports
}

const t = (key) =>
  ({
    "knowledge.channelIM": "Conversations",
    "knowledge.channelAgentAssist": "Agent Assist",
    "knowledge.channelAPI": "API",
    "knowledge.channelDebug": "Debug",
    "knowledge.sceneFirstResponse": "First response",
    "knowledge.sceneAssist": "Assisted reply",
    "knowledge.sceneQA": "Q&A",
    "knowledge.answerNormal": "Answered",
    "knowledge.answerNoAnswer": "No answer",
    "knowledge.answerFallback": "Fallback",
    "knowledge.answerBlocked": "Blocked",
    "knowledge.chunkFixed": "Fixed length",
    "knowledge.chunkStructured": "Structured chunks",
    "knowledge.chunkFAQ": "Q&A chunks",
    "knowledge.chunkSemantic": "Semantic chunks",
  })[key] ?? key

test("localizes knowledge retrieve enum labels from stable values", async () => {
  const {
    getKnowledgeAnswerStatusLabel,
    getKnowledgeChunkProviderLabel,
    getKnowledgeRetrieveChannelLabel,
    getKnowledgeRetrieveSceneLabel,
  } = await loadModule()

  assert.equal(getKnowledgeRetrieveChannelLabel("im", "\u5ba2\u670d\u4f1a\u8bdd", t), "Conversations")
  assert.equal(getKnowledgeRetrieveSceneLabel("first_response", "\u9996\u6b21\u56de\u590d", t), "First response")
  assert.equal(getKnowledgeAnswerStatusLabel(2, "\u65e0\u7b54\u6848", t), "No answer")
  assert.equal(getKnowledgeChunkProviderLabel("semantic", t), "Semantic chunks")
})
