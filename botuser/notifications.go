package botuser

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/maddevsio/comedian/model"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/olebedev/when"
	"github.com/olebedev/when/rules/en"
	"github.com/olebedev/when/rules/ru"
	log "github.com/sirupsen/logrus"
)

//NotifierThread struct to manage notifier goroutines
type NotifierThread struct {
	channel model.Channel
	quit    chan struct{}
}

// NotifyChannels reminds users of channels about upcoming or missing standups
func (bot *Bot) NotifyChannels(t time.Time) {
	if int(t.Weekday()) == 6 || int(t.Weekday()) == 0 {
		return
	}
	//TODO COM-1644
	channels, err := bot.db.ListChannels()
	if err != nil {
		log.Errorf("notifier: ListAllStandupTime failed: %v\n", err)
		return
	}

	// For each standup time, if standup time is now, start reminder
	for _, channel := range channels {
		if channel.TeamID != bot.properties.TeamID {
			continue
		}

		if channel.StandupTime == "" {
			continue
		}

		w := when.New(nil)
		w.Add(en.All...)
		w.Add(ru.All...)

		r, err := w.Parse(channel.StandupTime, time.Now())
		if err != nil {
			log.Errorf("Unable to parse channel standup time [%v]: [%v]", channel.StandupTime, err)
			continue
		}

		if r == nil {
			log.Errorf("Could not find matches. Channel standup time:  [%v]", channel.StandupTime)
			continue
		}

		warningTime := time.Unix(r.Time.Unix()-bot.properties.ReminderTime*60, 0)
		if t.Hour() == warningTime.Hour() && t.Minute() == warningTime.Minute() {
			err := bot.SendWarning(channel.ChannelID)
			if err != nil {
				log.Error(err)
			}
		}

		if t.Hour() == r.Time.Hour() && t.Minute() == r.Time.Minute() {
			nt := &NotifierThread{channel: channel, quit: make(chan struct{})}

			bot.wg.Add(1)
			go func(nt *NotifierThread) {
				err := bot.SendChannelNotification(nt)
				if err != nil {
					log.Error(err)
				}
				bot.wg.Done()
			}(nt)
			bot.AddNewNotifierThread(nt)
		}
	}
}

// SendWarning reminds users in chat about upcoming standups
func (bot *Bot) SendWarning(channelID string) error {
	standupers, err := bot.db.ListStandupers()
	if err != nil {
		return err
	}

	if len(standupers) == 0 {
		return nil
	}

	nonReportersIDs := []string{}
	for _, standuper := range standupers {
		if standuper.ChannelID == channelID && !bot.submittedStandupToday(standuper.UserID, standuper.ChannelID) && standuper.RoleInChannel != "pm" {
			nonReportersIDs = append(nonReportersIDs, "<@"+standuper.UserID+">")
		}
	}

	if len(nonReportersIDs) == 0 {
		return nil
	}

	minutes, err := bot.localizer.Localize(&i18n.LocalizeConfig{
		DefaultMessage: &i18n.Message{
			ID:    "minutes",
			Other: "",
		},
		PluralCount:  int(bot.properties.ReminderTime),
		TemplateData: map[string]interface{}{"time": bot.properties.ReminderTime},
	})
	if err != nil {
		log.Error(err)
	}

	warnNonReporters, err := bot.localizer.Localize(&i18n.LocalizeConfig{
		DefaultMessage: &i18n.Message{
			ID:    "warnNonReporters",
			Other: "",
		},
		PluralCount:  len(nonReportersIDs),
		TemplateData: map[string]interface{}{"user": nonReportersIDs[0], "users": strings.Join(nonReportersIDs, ", "), "minutes": minutes},
	})
	if err != nil {
		log.Error(err)
	}

	err = bot.SendMessage(channelID, warnNonReporters, nil)
	if err != nil {
		log.Error(err)
		return nil
	}

	return nil
}

//SendChannelNotification starts standup reminders and direct reminders to users
func (bot *Bot) SendChannelNotification(nt *NotifierThread) error {
	standupers, err := bot.db.ListChannelStandupers(nt.channel.ChannelID)
	if err != nil {
		return err
	}

	if len(standupers) == 0 {
		return nil
	}

	nonReporters := []model.Standuper{}
	for _, standuper := range standupers {
		if standuper.ChannelID == nt.channel.ChannelID && !bot.submittedStandupToday(standuper.UserID, standuper.ChannelID) && standuper.RoleInChannel != "pm" {
			nonReporters = append(nonReporters, standuper)
		}
	}

	if len(nonReporters) == 0 {
		return nil
	}

	var repeats int

	notifyNotAll := func() error {
		select {
		case <-nt.quit:
			return nil
		default:
			err := bot.notifyNotAll(nt.channel.ChannelID, &repeats)
			if err != nil {
				return err
			}
			return nil
		}
	}

	b := backoff.NewConstantBackOff(time.Duration(bot.properties.NotifierInterval) * time.Minute)
	err = backoff.Retry(notifyNotAll, b)
	if err != nil {
		log.Errorf("notifier: backoff.Retry failed: %v\n", err)
		return errors.New("BackOff failed")
	}
	return nil
}

func (bot *Bot) notifyNotAll(channelID string, repeats *int) error {

	if *repeats >= bot.properties.ReminderRepeatsMax {
		return nil
	}

	standupers, err := bot.db.ListChannelStandupers(channelID)
	if err != nil {
		return err
	}

	if len(standupers) == 0 {
		return nil
	}

	nonReporters := []string{}
	for _, standuper := range standupers {
		if !bot.submittedStandupToday(standuper.UserID, standuper.ChannelID) && standuper.RoleInChannel != "pm" {
			nonReporters = append(nonReporters, fmt.Sprintf("<@%v>", standuper.UserID))
		}
	}

	if len(nonReporters) == 0 {
		return nil
	}

	tagNonReporters, err := bot.localizer.Localize(&i18n.LocalizeConfig{
		DefaultMessage: &i18n.Message{
			ID:    "tagNonReporters",
			Other: "",
		},
		PluralCount:  len(nonReporters),
		TemplateData: map[string]interface{}{"user": nonReporters[0], "users": strings.Join(nonReporters, ", ")},
	})
	if err != nil {
		log.Error(err)
	}

	err = bot.SendMessage(channelID, tagNonReporters, nil)
	if err != nil {
		log.Error("SendMessage in notify not all failed: ", err)
	}
	*repeats++
	return errors.New("Continue backoff")

}

//AddNewNotifierThread adds to notifierThreads new thread
func (bot *Bot) AddNewNotifierThread(nt *NotifierThread) {
	bot.notifierThreads = append(bot.notifierThreads, nt)
}

//StopNotifierThread stops notifier thread of channel
func (bot *Bot) StopNotifierThread(nt *NotifierThread) {
	nt.quit <- struct{}{}
}

//FindNotifierThread returns object of NotifierThread and true if notifier thread by channel exist
func (bot *Bot) FindNotifierThread(channel model.Channel) (*NotifierThread, bool) {
	for _, nt := range bot.notifierThreads {
		if nt.channel.ID == channel.ID {
			return nt, true
		}
	}
	return &NotifierThread{}, false
}

//DeleteNotifierThreadFromList removes NotifierThread from list of threads
func (bot *Bot) DeleteNotifierThreadFromList(channel model.Channel) {
	position := 0
	for _, nt := range bot.notifierThreads {
		if nt.channel.ID == channel.ID {
			l1 := bot.notifierThreads[:position]
			l2 := bot.notifierThreads[position+1:]
			result := append(l1, l2...)
			if position > 0 {
				position = position - 1
			}
			bot.notifierThreads = result
		}
		position++
	}
}
