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
	var nextJob string
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
			nextJob = filepath.Base(path)
		} else if t.Before(time.Now()) {
			fmt.Printf("Expired: %s\n", path)
		}
		return nil
	})
	if nextCmd != "" {
		fmt.Printf("Next: %s %v %v\n", nextJob, nextTime, nextTime.Sub(time.Now()))
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
	t, err := parseDateTime(line)
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

func parseDateTime(input string) (time.Time, error) {
	specs := strings.Split(input, " ")
	for i, spec := range specs {
		specs[i] = strings.TrimSpace(spec)
	}
	var year, month, day, hour, minute, second, dayOfWeek int
	var isRepeat, isHourRepeat, isDayRepeat, isWeekRepeat, isMonthRepeat bool
	var ret time.Time
	_ = dayOfWeek
	_ = isDayRepeat
	_ = isWeekRepeat
	_ = isMonthRepeat

	datePattern := regexp.MustCompile(`^([0-9]{2})?[0-9]{2}-[0-9]{1,2}-[0-9]{1,2}|[0-9]{1,2}-[0-9]{1,2}$`)
	timePattern := regexp.MustCompile(`^[0-9]{1,2}:[0-9]{1,2}(:[0-9]{1,2})?$`)
	minuteSecondPattern := regexp.MustCompile(`^[0-9]{1,2}(:[0-9]{1,2})?$`)
	for _, spec := range specs {
		switch {
		case !isRepeat && datePattern.MatchString(spec): // date
			err := parseDate(spec, &year, &month, &day)
			if err != nil {
				return time.Now(), err
			}
		case !isRepeat && timePattern.MatchString(spec): // time
			err := parseTime(spec, &hour, &minute, &second)
			if err != nil {
				return time.Now(), err
			}
		case spec == "every": // repeat
			isRepeat = true
		case spec == "hour" && isRepeat: // hour repeat
			isHourRepeat = true
		case isHourRepeat && minuteSecondPattern.MatchString(spec):
			err := parseMinuteSecond(spec, &minute, &second)
			if err != nil {
				return time.Now(), err
			}
		default:
			fmt.Printf("Error time spec: %s\n", spec)
		}
	}

	if isHourRepeat {
		ret = nextHourRepeat(minute, second)
	} else if !isRepeat {
		ret = time.Date(year, time.Month(month), day, hour, minute, second, 0, time.Local)
	} else {
		return time.Now(), errors.New("invalid time spec")
	}
	return ret, nil
}

func parseDate(spec string, year, month, day *int) error {
	dashCount := strings.Count(spec, "-")
	if dashCount == 2 {
		n, err := fmt.Sscanf(spec, "%d-%d-%d", year, month, day)
		if n != 3 || err != nil {
			return errors.New("parse date")
		}
	} else if dashCount == 1 {
		n, err := fmt.Sscanf(spec, "%d-%d", month, day)
		if n != 3 || err != nil {
			return errors.New("parse date")
		}
		*year = time.Now().Year()
	}
	return nil
}

func parseTime(spec string, hour, minute, second *int) error {
	semiCount := strings.Count(spec, ":")
	if semiCount == 2 {
		n, err := fmt.Sscanf(spec, "%d:%d:%d", hour, minute, second)
		if n != 3 || err != nil {
			return errors.New("parse time")
		}
	} else if semiCount == 1 {
		n, err := fmt.Sscanf(spec, "%d:%d", hour, minute)
		if n != 2 || err != nil {
			return errors.New("parse time")
		}
	}
	return nil
}

func parseMinuteSecond(spec string, minute, second *int) error {
	if strings.Contains(spec, ":") {
		n, err := fmt.Sscanf(spec, "%d:%d", minute, second)
		if n != 2 || err != nil {
			return errors.New("parse minute:second")
		}
	} else {
		n, err := fmt.Sscanf(spec, "%d", minute)
		if n != 1 || err != nil {
			return errors.New("parse minute")
		}
	}
	return nil
}

func nextHourRepeat(minute, second int) time.Time {
	y, m, d := time.Now().Date()
	h, _, _ := time.Now().Clock()
	t := time.Date(y, m, d, h, minute, second, 0, time.Local)
	if t.Before(time.Now()) {
		t = t.Add(time.Hour * 1)
	}
	return t
}
