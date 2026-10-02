# Comment batch mode

## Agreed UX

- Ctrl+C, outside editable fields, enters single-comment mode immediately; a second Ctrl+C within 350 ms upgrades the same gesture to batch mode. Ctrl+C in batch mode turns it off, and in single-comment mode a Ctrl+C after the 350 ms window turns it off. Ctrl+B is removed. Selected text and editable fields copy normally. Blue accents replace gold on the comment button, selection outline, cursor, and dialog while batch mode is active.
- Mode controls collection, not the lifetime of the unsent batch. Turning it off never submits or discards drafts. Turning it back on resumes collection into the same batch.
- New comments saved during batch mode join the batch. Existing drafts never join automatically.
- In batch mode the editor has `Exclude from batch`, `Save draft`, and `Send now`. Ctrl+Enter saves. Excluded comments remain standalone draft notes. Send now submits only that comment.
- Left of the comments button, show `N comments in draft · Send` for eligible batch members. This remains available with batch mode off. Send submits only members and leaves the mode unchanged.
- Existing draft dialogs offer `Add to the batch` only when a non-empty unsent batch exists, even with batch mode off.
- Submitted comments remain grouped by batch, with progress such as `3 of 5 ready for review`. Replies and human resolution remain per-thread.
- Agents receive the entire submitted batch as one coordinated task, for related changes or independent fixes.

## Architecture

Use the existing submitted batch IDs and atomic receive workflow. Add durable explicit draft membership (`inBatch`) to the comment store: one unsent collection per project. Draft membership is independent of the browser's per-tab mode state. Older drafts default to nonmembers. Clearing membership when a draft is submitted prevents stale collection state.

Keep existing single-comment submission and legacy CLI submission compatible. The browser batch Send uses a dedicated membership-filtered submit path, never the legacy submit-all-drafts operation. Persist mode in session storage alongside existing comment-mode restoration. Submitted grouping uses the existing `batchId` and thread state.

## Atomic implementation slices

1. **Plan** — commit this agreement and implementation sequence.
2. **Store** — additive SQLite migration, draft membership, draft-only membership changes, atomic selected-batch submission, restart and isolation tests.
3. **API** — creation membership, membership action, batch-filtered submit, origin/state/error tests.
4. **Browser collection** — routing, Ctrl+C double-tap, blue accents, editor actions, persistent toolbar, draft membership controls, focused JS/browser tests.
5. **Review grouping** — batch groups and progress without losing per-thread actions; regression tests. May share the browser slice if closely coupled.
6. **Workflow/docs** — coordinated batch processing guidance, per-thread replies/review, updated user documentation and guidance tests.
7. **Verification/fixes** — full tests, independent review, focused fixes in separate conventional commits.

## Verification

- Existing drafts and excluded notes are never included by batch Send.
- Toggle off, reload, toggle on, add another comment, then Send: original membership survives.
- Send now never flushes the pending batch.
- The Ctrl+C double-tap leaves editable fields, selected text, and normal browser editing alone; Ctrl+Enter saves in batch mode.
- Failed saves/submits preserve recoverable drafts and do not close the editor prematurely.
- Membership changes reject nondrafts; submission is atomic and guards invalid/stale state.
- Submission keeps batch mode active or inactive exactly as it was.
- Grouped progress includes resolved members while threads retain independent actions.
- Run Go suite, Node-backed widget regressions, race checks where practical, and independent code review.
