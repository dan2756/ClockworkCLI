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
every 5m        ./scripts/sync.sh
every 1h30m     rsync -a ~/notes/ backup:/notes/
```

Then:

```sh
cronlite check   # validate the file
cronlite run     # run the scheduler until Ctrl-C
```

Each line is a schedule followed by a command. Commands run with `sh -c`
from the directory that contains the task file. Durations use Go syntax
(`10s`, `5m`, `1h30m`) plus `d` for days (`2d`).

cronlite reads `./cronlite.txt` if it exists, otherwise
`~/.config/cronlite/tasks`. Use `-f FILE` to pick another file.

## License

MIT
