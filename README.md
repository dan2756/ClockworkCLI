# cronlite

Runs commands on intervals from a plain text file.

cronlite is a tiny foreground scheduler for the jobs you would rather not
write a crontab or a systemd timer for: keep a list of commands in a text
file next to your project, run `cronlite run`, and watch their output scroll
by with timestamps and task names.

## Install

```sh
go install github.com/dan2756/cronlite@latest
```

## Usage

Create `cronlite.txt`:

```
# comments and blank lines are fine
every 5m                    ./scripts/sync.sh
every 1h30m                 rsync -a ~/notes/ backup:/notes/
cron "*/15 9-17 * * 1-5"    ./check-queue.sh
```

Then:

```sh
cronlite check   # validate the file
cronlite run     # run the scheduler until Ctrl-C
```

Each line is a schedule followed by a command. Commands run with `sh -c`
from the directory that contains the task file.

- `every <duration>` uses Go duration syntax (`10s`, `5m`, `1h30m`) plus `d`
  for days (`2d`).
- `cron "<expr>"` takes a standard five-field cron expression: lists, ranges,
  steps and month/weekday names (`jan`, `mon`) all work.

To see when a cron expression fires:

```sh
$ cronlite next "*/15 9-17 * * 1-5" -n 3
Mon 2026-01-26 09:00 EST  in 13h9m
Mon 2026-01-26 09:15 EST  in 13h24m
Mon 2026-01-26 09:30 EST  in 13h39m
```

cronlite reads `./cronlite.txt` if it exists, otherwise
`~/.config/cronlite/tasks`. Use `-f FILE` to pick another file.

## License

MIT
