# Publishing to the public repo

The public repo is github.com/karmiphuc/chatgpt-dots-fb-messenger-cli. It is published by web
upload, not `git push`. The local clone's history is separate, and the GitHub history is made
of upload commits.

The guardrails behind every step are in [../leak-guardrails.md](../leak-guardrails.md).

## 1. Commit and gate locally

```bash
bash scripts/check-public.sh             # must print "check-public: clean"
go test -race -count=1 ./...
bash scripts/test-leak-guard.sh          # must end with 0 failed
git status --short                       # must be empty: export refuses uncommitted changes
bash scripts/export-public.sh --out ~/fbm-publish-$(date +%Y%m%d-%H%M)
```

`export-public.sh` prints the folder and the commit SHA. Upload **only** from that folder.

## 2. List what changed since the last publish

Compare the local tree with the published one:

```bash
git diff --name-status <last-published-commit>..HEAD
```

The last-published local commit is recorded at the end of this file.

## 3. Upload through the GitHub web UI

- **Existing folder.** Open
  `https://github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/upload/main/<folder>`.
  Choose the files from the export folder, wait until every file is listed, write a
  conventional commit message, and commit.
  - Wait for the list before committing. Committing early can silently land nothing.
- **Folder that does not exist on GitHub yet.** The upload page redirects to the repo root.
  Create the folder by opening the new-file page with a first file prefilled:
  `https://github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/new/main?filename=<dir>%2F<file>&value=<url-encoded content>`.
  Subfolders of an existing top-level folder upload fine.
- **Dot-files and `.github/`.** Finder hides them; press Cmd+Shift+. to show them. Upload
  workflow files last, so the first run sees the complete tree.
- **Executable bits are lost on upload.** Docs therefore run scripts with `bash scripts/...`,
  and `install.sh` restores the hooks' executable bit.

Agents doing this through a browser use the same pages:

1. Use a file-input upload tool on `#upload-manifest-files-input`.
2. Wait about 8 seconds.
3. Check that every file name is listed.
4. Set `#commit-summary-input` and click **Commit changes**.

## 4. Verify byte-for-byte

Compare every local blob SHA with the GitHub tree API. File contents must match; file modes
will differ for scripts.

```bash
git ls-files -s | awk '{print $4" "$2}'          # local path -> blob sha
```

Fetch the remote tree from
`https://api.github.com/repos/karmiphuc/chatgpt-dots-fb-messenger-cli/git/trees/main?recursive=1`,
then compare. There must be no missing, extra or mismatched paths.

Then open the Actions tab and confirm that the `leak-guard` run for the last upload commit
passed.

## 5. Record it

Update [../PROGRESS.md](../PROGRESS.md) and the "Last published" line below.

Last published: local commit `94760cd`, followed by the commit that updates this line, uploaded
2026-10-17. The full tree digest matched the GitHub tree API, 77/77 files, and `leak-guard`
passed on every upload commit.
