# Architecture diagrams

Standalone Mermaid sources embedded verbatim in [`ARCHITECTURE.md`](../../ARCHITECTURE.md). Edit the `.mmd` file and the matching fenced block together.

| Diagram | Section in ARCHITECTURE.md | Shows |
|---|---|---|
| [`layer-model.mmd`](mermaid/layer-model.mmd) | Layer model | Worker (composition root) → facade → provider adapters → connector cores → support leaves |
| [`package-dependencies.mmd`](mermaid/package-dependencies.mmd) | Package dependency graph | Every module-internal import, mirroring `.go-arch-lint.yml` |
| [`execution-flow.mmd`](mermaid/execution-flow.mmd) | Execution flow | One `storage` call: secret resolution, validation, client cache, adapter, output |
| [`docref-flow.mmd`](mermaid/docref-flow.mmd) | Document refs | How `storage` and `send-email` share documents: content in S3, reference metadata in Valkey, verified on every resolution |
| [`internal-auth-flow.mmd`](mermaid/internal-auth-flow.mmd) | Internal-auth flow | `rest-call` alias resolution and the two mandatory headers |
| [`drive-upload-flow.mmd`](mermaid/drive-upload-flow.mmd) | Drive document registry | Two concurrent uploads of one document: claim, in-progress refusal, owned write, completion |
| [`documentation.mmd`](mermaid/documentation.mmd) | Documentation assets | Where each document lives and what renders it |

Render locally in a Mermaid-aware IDE (VS Code + Mermaid Preview, IntelliJ + Mermaid plugin) or paste into [mermaid.live](https://mermaid.live).
