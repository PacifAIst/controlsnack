# Putting the site on Cloudflare (free)

The site is the folder `site/`: plain files, no build step, no script from other places. Cloudflare serves it for free, from a project that is connected to this repository by Git: every push to `main` is a new deploy of the folder (D-114). Nobody uploads anything by hand.

The easy steps on the site are the steps that were run: TESTING.md Parts A to C passed on native Windows 10 with a local model and with a hosted one (D-110, D-111). The site is the pages `index.html` and `docs.html` (the documentation, with a guide for newcomers), the screenshots in `img/`, and the files they use.

## What a push to `main` does

A push to `main` is published, whatever it holds: nothing in this repository holds a deploy back until the checks have passed. So the checks below run before the push, on your machine, and a change under `site/` is public as soon as Cloudflare has deployed it.

## Before each push that changes `site/`

1. Check that the "Pre-release" paragraph of the first section and the table of models say what is true by then (what has been run, where), and that step 2 of the easy steps names the archives the newest release has.
2. `go test ./internal/sitecheck`. It checks that nothing is loaded from elsewhere (the only links to another address are the two to the source on GitHub and to its releases page), that the headers are strict, that both pages work in a browser at desktop and phone width, and that `docs.html` describes every command, flag, configuration key, built-in rule, tool and exit code of the program, that its examples load, and that every screenshot exists, has its true size and a description.
3. If a flag or a command changes, the check fails until `docs.html` (and the guide in `docs/GETTING-STARTED.md`, which shows the same screenshots) says so. The screenshots are real: retake them when the viewer or the terminal output changes in a way a reader would notice.

## How to tell that a push went live

The dashboard of the Cloudflare project lists every deploy with its commit and its log. From a terminal: the pages are served as they are in the repository, byte for byte, so once the deploy of a commit has finished these two sums are the same:

```
curl -s https://controlsnack.smartcityjaen.workers.dev/docs | sha256sum
sha256sum site/docs.html
```

Pages are cached for five minutes at most (`site/_headers`), so wait that long after a deploy before you compare. If the sums still differ, that commit was not deployed: the log of the project's latest deploy says why.

## Setting it up (once)

1. Cloudflare dashboard → **Workers & Pages** → **Create**, and connect the project to Git: the repository `PacifAIst/controlsnack` (Cloudflare's GitHub app has to be allowed to read it).
2. The settings the project needs: production branch `main`, no build command, and the folder `site` as the one that is served (the home page is `site/index.html`, not one at the root of the repository).
3. Deploy. The site is then at the address Cloudflare gives it: this project's is `https://controlsnack.smartcityjaen.workers.dev`.

The repository holds no Cloudflare configuration file, only `site/_headers`, which Cloudflare reads: those settings are the project's own, in the dashboard. The dashboard's wording changes from time to time; the settings above are what matter.

## The address

The site lives at `https://controlsnack.smartcityjaen.workers.dev` and is deployed by Cloudflare from this repository on every push to `main` (D-114). There is no custom domain, no build step and no workflow in this repository that publishes it (the Git connection is Cloudflare's own): the README, the guide, the documentation and the release notes name that address, and a test (`internal/sitecheck`) fails if one of the addresses given up (a `.com`, and the `pages.dev` one) comes back into them.

## What the site does and does not do

- It sets no cookies, loads no font, script, image or style from another address, and has no analytics. The content security policy in `site/_headers` forbids it.
- The test section keeps what is typed in the visitor's browser (`localStorage`) and builds a report there. The page never sends it: the visitor copies it or downloads it. A form that posts to a server would need a back end and a privacy notice, and is not part of it (D-106).
