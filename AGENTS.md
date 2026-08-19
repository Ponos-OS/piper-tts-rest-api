The coding guidelines for this project are maintained in [CONTRIBUTING.md](.github/CONTRIBUTING.md). Please read and follow that file.

- Comments only when necessary.
- Be concise, short README, no emojis.
- No extra feature, focus on what has been asked.
- Do not add production API parameters, hooks, dependencies, abstractions, or behavior solely to make tests easier to write. Tests must adapt to the production design, not the reverse.
- Treat existing domain names, status values, event fields, and their JSDoc as authoritative. Do not repurpose a field or invent something without explicit approval. Ask if the intended semantics are unclear.
- Before reporting completion, run the relevant build and tests. If pre-existing or user-introduced tests fail, report the exact failure without modifying unrelated code.
- Do not expand an implementation step beyond the requested scope. Record deferred work in the plan instead of implementing it early.
