# youtube-unsubscribe

A small Go CLI that lists your YouTube subscriptions and can unsubscribe from all of them.

The tool uses the YouTube Data API v3. By default it performs a dry run and only prints the subscriptions it found. You must pass `--execute` and confirm before it deletes anything.

## Setup

1. Create or choose a Google Cloud project.
2. Enable the YouTube Data API v3.
3. Configure an OAuth consent screen.
4. Create an OAuth client ID. A "Desktop app" client is usually the easiest option.
5. Download the OAuth client JSON and save it in this directory as `credentials.json`.

## Usage

Install dependencies:

```sh
go mod tidy
```

Preview what would be removed:

```sh
go run . --credentials credentials.json
```

Unsubscribe from every listed channel:

```sh
go run . --credentials credentials.json --execute
```

Skip the confirmation prompt:

```sh
go run . --credentials credentials.json --execute --yes
```

Process only a small batch:

```sh
go run . --credentials credentials.json --limit 10 --execute
```

The first run opens an OAuth authorization URL. After you approve access, the token is cached in `token.json` with file mode `0600`.

## Notes

`subscriptions.delete` costs 50 YouTube API quota units per subscription. A default daily quota of 10,000 units can delete roughly 200 subscriptions, minus the small cost of listing them.
