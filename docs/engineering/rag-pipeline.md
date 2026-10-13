# RAG and Answerability

## Local stack and flow

The demonstrated local setup uses a DeepSeek-compatible chat model, BAAI/bge-m3 embeddings and Qdrant. These are privately configured integrations; model weights and credentials are not bundled. The knowledge base, retrieval pipeline, and Answerability Gate form the KestrelDesk retrieval stack.

1. Knowledge documents are indexed into chunks, embeddings and vector metadata.
2. The selected knowledge bases and retrieval policies determine eligible evidence.
3. Query embeddings drive vector search; Qdrant filters can constrain knowledge-base and document IDs.
4. Results are hydrated and assembled within context limits by the runtime retriever.
5. The Agent/workflow uses evidence and its reply policy to produce a grounded answer or knowledge-insufficiency response.
6. Execution/retrieval records allow inspection of the run and evidence rather than judging success from a plausible answer alone.

Source: [RAG retrieval](../../internal/ai/rag/retrieve.go), [Qdrant adapter](../../internal/ai/rag/vectordb/qdrant.go), [runtime retriever](../../internal/ai/runtime/retrievers/knowledge_retriever.go), [workflow executor](../../internal/ai/runtime/workflow/executor.go).

## What the gate actually checks

The workflow `executeAnswerabilityGate` marks `answerable` when retrieved knowledge items are available and `unanswerable` otherwise. This is an evidence-availability gate, not a semantic correctness guarantee. An Agent response refusing an unsupported claim should not automatically be attributed to execution of that workflow node; inspect the run's runtime and steps.

## Observed local evidence

| Scenario | Evidence / expected result |
| --- | --- |
| “你们的客服工作时间是什么？” | Visually observed answer: Monday–Friday 09:00–17:00 Asia/Shanghai; within one business day response, consistent with the synthetic policy |
| “支持哪些附件格式？单个文件最大多大？” | Visually observed: PNG, JPG, PDF; up to 10 MB per file, consistent with the policy |
| Unsupported company headquarters address | Visually observed knowledge insufficiency; no fabricated address |
| Unsupported refund policy: “没有订单信息时，能保证全额退款吗？” | Observed after branding on 2026-10-08 23:30:16: the Agent states the available policy does not cover refund guarantees/conditions and cannot confirm a guarantee |

The original insufficiency example concerned an address. A new synthetic service-hours question and the refund question were sent after branding; their observed replies informed these notes. The corresponding customer screenshot is not included in this candidate. **RAG PASS / Answerability PASS** refer to these local observations, not a claim that every unsupported question is handled correctly.

The synthetic support-policy document is test information. Its stated attachment formats are policy content, not proof of the deferred attachment authorization controls. No retrieval accuracy, latency percentile, model reliability or production-scale metric is claimed.

## Inspecting a run

In the administrator browser open `/dashboard/agent-runs`, select the corresponding run and inspect status, retrieval steps, input evidence and output. A completed run plus retrieval evidence supports the RAG path. Quality-review fields remaining `Unknown` are not test failures and must not be relabeled automatically. Compare the knowledge-base statement with the reply; avoid secret-bearing model/channel settings.

> This file has been modified for the KestrelDesk project.
