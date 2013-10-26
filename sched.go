package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var signals = make(chan os.Signal)

func init() {
	signal.Notify(signals, syscall.SIGUSR1)
}

func main() {
	user, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	jobDir := filepath.Join(user.HomeDir, ".sched")
	_, err = os.Stat(jobDir)
	if os.IsNotExist(err) {
		err = os.Mkdir(jobDir, os.ModePerm)
		if err != nil {
			log.Fatal(err)
		}
	} else if err != nil {
		log.Fatal(err)
	}
	for {
		hasJob := checkJobs(jobDir)
		if hasJob {
			continue
		} else {
			select {
			case <-signals:
			case <-time.After(time.Minute * 1):
			}
			continue
		}
	}
}

func checkJobs(jobDir string) (hasJob bool) {
	nextTime := time.Date(9999, 1, 1, 0, 0, 0, 0, time.Local)
	var nextCmd string
	var nextArgs []string
	filepath.Walk(jobDir, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		input, err := os.Open(path)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return nil
		}
		t, cmd, args, err := parse(bufio.NewReader(input))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return nil
		}
		if t.After(time.Now()) && t.Before(nextTime) {
			nextTime = t
			nextCmd = cmd
			nextArgs = args
		} else if t.Before(time.Now()) {
			fmt.Printf("Job expired: skip %s\n", path)
		}
		return nil
	})
	if nextCmd != "" {
		fmt.Printf("Next: %v, now %v\n", nextTime, time.Now())
		tick := time.NewTicker(time.Minute * 1)
		select {
		case <-time.After(nextTime.Sub(time.Now())):
			fmt.Printf("%v: Run %s %v\n", time.Now(), nextCmd, nextArgs)
			cmd := exec.Command(nextCmd, nextArgs...)
			cmd.Start()
			return true
		case <-signals:
			return true
		case <-tick.C: // TODO
			return true
		}
	}
	return false
}

func parse(input *bufio.Reader) (time.Time, string, []string, error) {
	line, err := input.ReadString('\n')
	if err != nil {
		return time.Now(), "", nil, errors.New("read line")
	}
	t, err := parseTime(line)
	if err != nil {
		return time.Now(), "", nil, errors.New("parse datetime")
	}
	cmd, err := input.ReadString('\n')
	if err != nil && err != io.EOF {
		return time.Now(), "", nil, errors.New("parse command")
	}
	cmd = strings.TrimSpace(cmd)
	args := make([]string, 0)
	for {
		arg, err := input.ReadString('\n')
		if err == io.EOF {
			args = append(args, arg)
			break
		} else if err != nil {
			break
		}
		args = append(args, arg)
	}
	return t, cmd, args, nil
}

func parseTime(input string) (time.Time, error) {
	specs := strings.Split(input, " ")
	for i, spec := range specs {
		specs[i] = strings.TrimSpace(spec)
	}
	var year, month, day, hour, minute, second int

	datePattern := regexp.MustCompile(`^([0-9]{2})?[0-9]{2}-[0-9]{2}-[0-9]{2}|[0-9]{2}-[0-9]{2}$`)
	timePattern := regexp.MustCompile(`^[0-9]{2}:[0-9]{2}(:[0-9]{2})?`)
	for _, spec := range specs {
		switch {
		case datePattern.MatchString(spec): // date
			dashCount := strings.Count(spec, "-")
			if dashCount == 2 {
				n, err := fmt.Sscanf(spec, "%d-%d-%d", &year, &month, &day)
				if n != 3 || err != nil {
					return time.Now(), errors.New("parse date")
				}
			} else if dashCount == 1 {
				n, err := fmt.Sscanf(spec, "%d-%d", &month, &day)
				if n != 3 || err != nil {
					return time.Now(), errors.New("parse date")
				}
				year = time.Now().Year()
			}
		case timePattern.MatchString(spec): // time
			semiCount := strings.Count(spec, ":")
			if semiCount == 2 {
				n, err := fmt.Sscanf(spec, "%d:%d:%d", &hour, &minute, &second)
				if n != 3 || err != nil {
					return time.Now(), errors.New("parse time")
				}
			} else if semiCount == 1 {
				n, err := fmt.Sscanf(spec, "%d:%d", &hour, &minute)
				if n != 2 || err != nil {
					return time.Now(), errors.New("parse time")
				}
			}
		default:
			fmt.Printf("Error time spec: %s\n", spec)
		}
	}

	t := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.Local)
	return t, nil
}
