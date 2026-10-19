# Putting the site on Cloudflare (free)

The site is the folder `site/`: plain files, no build step, no script from other places. Cloudflare serves it for free (the dashboard offers it as Pages, or as a Workers project with static assets; both read the folder as it is).

The easy steps on the site are the steps that were run: TESTING.md Parts A to C passed on native Windows 10 with a local model and with a hosted one (D-110, D-111). Publishing does not need the GitHub repository: Direct Upload works without it. The site is the pages `index.html` and `docs.html` (the documentation, with a guide for newcomers), the screenshots in `img/`, and the files they use; upload the whole folder.

## Before each publish

1. Check that the "Pre-release" paragraph of the first section and the table of models say what is true by then (what has been run, where), and point step 2 at the release archives if there are any.
2. `go test ./internal/sitecheck`. It checks that nothing is loaded from elsewhere (the only link to another address is the one to the source on GitHub), that the headers are strict, that both pages work in a browser at desktop and phone width, and that `docs.html` describes every command, flag, configuration key, built-in rule, tool and exit code of the program, that its examples load, and that every screenshot exists, has its true size and a description.
3. If a flag or a command changes, the check fails until `docs.html` (and the guide in `docs/GETTING-STARTED.md`, which shows the same screenshots) says so. The screenshots are real: retake them when the viewer or the terminal output changes in a way a reader would notice.

## Publish, by dragging the folder

1. Cloudflare dashboard → **Workers & Pages** → **Create** → upload the files (**Upload assets**, or drag the folder).
2. Name the project `controlsnack`, upload the `site` folder, **Deploy**. The site is then at the address Cloudflare gives it: this project's is `https://controlsnack.smartcityjaen.workers.dev`.

or from a terminal, with Node:

```
npx wrangler pages deploy site --project-name controlsnack
```

## The address

The site lives at `https://controlsnack.smartcityjaen.workers.dev` and is uploaded by hand, as above, when its owner decides (D-114). There is no custom domain, no build step and no workflow in this repository that publishes it: the README, the guide, the documentation and the release notes name that address, and a test (`internal/sitecheck`) fails if one of the addresses given up (a `.com`, and the `pages.dev` one) comes back into them.

## What the site does and does not do

- It sets no cookies, loads no font, script, image or style from another address, and has no analytics. The content security policy in `site/_headers` forbids it.
- The test section keeps what is typed in the visitor's browser (`localStorage`) and builds a report there. The page never sends it: the visitor copies it or downloads it. A form that posts to a server would need a back end and a privacy notice, and is not part of it (D-106).
