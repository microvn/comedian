package reporting

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/nlopes/slack"
	"github.com/sirupsen/logrus"
	"gitlab.com/team-monitoring/comedian/bot"
	"gitlab.com/team-monitoring/comedian/collector"
	"gitlab.com/team-monitoring/comedian/model"
	"gitlab.com/team-monitoring/comedian/utils"
)

//Reporter provides db and translation to functions
type Reporter struct {
	bot                  *bot.Bot
	yesterdayReportFired bool
	weeklyReportFired    bool
}

// NewReporter creates a new reporter instance
func NewReporter(bot *bot.Bot) (Reporter, error) {
	reporter := Reporter{bot: bot}
	return reporter, nil
}

// Start starts all team monitoring treads
func (r *Reporter) Start() {

	dailyReporting := time.NewTicker(time.Second * 60).C
	weeklyReporting := time.NewTicker(time.Second * 60).C
	for {
		select {
		case <-dailyReporting:
			r.CallDisplayYesterdayTeamReport()
		case <-weeklyReporting:
			r.CallDisplayWeeklyTeamReport()
		}
	}
}

// CallDisplayYesterdayTeamReport calls displayYesterdayTeamReport
func (r *Reporter) CallDisplayYesterdayTeamReport() {
	localizer := i18n.NewLocalizer(r.bot.Bundle, r.bot.CP.Language)

	hour, minute, err := formatTime(r.bot.CP.ReportTime)
	if err != nil {
		logrus.Error(err)
		return
	}
	if time.Now().Hour() == hour && time.Now().Minute() == minute {
		_, err := r.displayYesterdayTeamReport()
		if err != nil {
			logrus.Error("Error in displayYesterdayTeamReport: ", err)
			yesterdayReportError := localizer.MustLocalize(&i18n.LocalizeConfig{
				DefaultMessage: &i18n.Message{
					ID:          "YesterdayReportError",
					Description: "Displays a message if sending yesterday report failed",
					Other:       "Error sending yesterday report: {{.error}}",
				},
				TemplateData: map[string]interface{}{
					"error": err,
				},
			})
			r.bot.SendUserMessage(r.bot.CP.ManagerSlackUserID, yesterdayReportError)
			return
		}
	}
}

// CallDisplayWeeklyTeamReport calls displayWeeklyTeamReport
func (r *Reporter) CallDisplayWeeklyTeamReport() {
	localizer := i18n.NewLocalizer(r.bot.Bundle, r.bot.CP.Language)

	if int(time.Now().Weekday()) != 0 {
		return
	}
	hour, minute, err := formatTime(r.bot.CP.ReportTime)
	if err != nil {
		logrus.Error(err)
		return
	}

	if time.Now().Hour() == hour && time.Now().Minute() == minute {
		_, err = r.displayWeeklyTeamReport()
		if err != nil {
			logrus.Error("Error in displayWeeklyTeamReport: ", err)
			weeklyReportError := localizer.MustLocalize(&i18n.LocalizeConfig{
				DefaultMessage: &i18n.Message{
					ID:          "WeeklyReportError",
					Description: "Displays a message if sending weekly report failed",
					Other:       "Error sending weekly report: {{.error}}",
				},
				TemplateData: map[string]interface{}{
					"error": err,
				},
			})
			r.bot.SendUserMessage(r.bot.CP.ManagerSlackUserID, weeklyReportError)
		}
		r.weeklyReportFired = true
	}
}

// displayYesterdayTeamReport generates report on users who submit standups
func (r *Reporter) displayYesterdayTeamReport() (FinalReport string, err error) {
	localizer := i18n.NewLocalizer(r.bot.Bundle, r.bot.CP.Language)

	var allReports []slack.Attachment

	channels, err := r.bot.DB.GetAllChannels()
	if err != nil {
		logrus.Errorf("GetAllChannels failed: %v", err)
		return FinalReport, err
	}

	reportHeader := localizer.MustLocalize(&i18n.LocalizeConfig{
		DefaultMessage: &i18n.Message{
			ID:          "ReportHeader",
			Description: "Displays a header of yesterday report",
			Other:       "Yesterday report",
		},
	})

	for _, channel := range channels {
		var attachments []slack.Attachment
		var attachmentsPull []model.AttachmentItem

		channelMembers, err := r.bot.DB.ListChannelMembers(channel.ChannelID)
		if err != nil {
			logrus.Errorf("ListChannelMembers failed for channel %v: %v", channel.ChannelName, err)
			continue
		}

		if len(channelMembers) == 0 {
			logrus.Infof("Skip %v channel", channel.ChannelID)
			continue
		}

		for _, member := range channelMembers {
			var attachment slack.Attachment
			var attachmentFields []slack.AttachmentField
			var worklogs, commits, standup string
			var worklogsPoints, commitsPoints, standupPoints int

			UserInfo, err := r.bot.DB.SelectUser(member.UserID)
			if err != nil {
				logrus.Errorf("SelectUser failed for  user %v: %v", UserInfo.UserName, err)
				continue
			}

			dataOnUser, dataOnUserInProject, collectorError := r.GetCollectorDataOnMember(member, time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, -1))

			if collectorError == nil {
				worklogs, worklogsPoints = r.processWorklogs(dataOnUser.Worklogs, dataOnUserInProject.Worklogs)
				commits, commitsPoints = r.processCommits(dataOnUser.Commits, dataOnUserInProject.Commits)
			}

			if member.RoleInChannel == "pm" || member.RoleInChannel == "designer" {
				commits = ""
			}

			if r.bot.CP.CollectorEnabled == false || collectorError != nil {
				worklogs = ""
				worklogsPoints++
				commits = ""
				commitsPoints++
			}

			standup, standupPoints = r.processStandup(member)

			fieldValue := worklogs + commits + standup

			//if there is nothing to show, do not create attachment
			if fieldValue == "" {
				continue
			}

			attachmentFields = append(attachmentFields, slack.AttachmentField{
				Value: fieldValue,
				Short: false,
			})

			points := worklogsPoints + commitsPoints + standupPoints

			//attachment text will be depend on worklogsPoints,commitsPoints and standupPoints
			if points >= 3 {
				notTagStanduper := localizer.MustLocalize(&i18n.LocalizeConfig{
					DefaultMessage: &i18n.Message{
						ID:          "NotTagStanduper",
						Description: "Displays a message without a user tag",
						Other:       "{{.user}} in #{{.channel}}",
					},
					TemplateData: map[string]interface{}{
						"user":    UserInfo.RealName,
						"channel": channel.ChannelName,
					},
				})
				attachment.Text = notTagStanduper
			} else {
				tagStanduper := localizer.MustLocalize(&i18n.LocalizeConfig{
					DefaultMessage: &i18n.Message{
						ID:          "TagStanduper",
						Description: "Displays a message with a user tag",
						Other:       "<@{{.user}}> in #{{.channel}}",
					},
					TemplateData: map[string]interface{}{
						"user":    member.UserID,
						"channel": channel.ChannelName,
					},
				})
				attachment.Text = tagStanduper
			}

			switch points {
			case 0:
				attachment.Color = "danger"
			case 1, 2:
				attachment.Color = "warning"
			case 3:
				attachment.Color = "good"
			}

			if int(time.Now().Weekday()) == 0 || int(time.Now().Weekday()) == 1 {
				attachment.Color = "good"
			}

			attachment.Fields = attachmentFields

			item := model.AttachmentItem{
				SlackAttachment: attachment,
				Points:          dataOnUserInProject.Worklogs,
			}

			attachmentsPull = append(attachmentsPull, item)
		}

		if len(attachmentsPull) == 0 {
			continue
		}

		attachments = r.sortReportEntries(attachmentsPull)

		r.bot.SendMessage(channel.ChannelID, reportHeader, attachments)

		allReports = append(allReports, attachments...)
	}

	if len(allReports) == 0 {
		return
	}

	r.bot.SendMessage(r.bot.CP.ReportingChannel, reportHeader, allReports)
	FinalReport = fmt.Sprintf(reportHeader, allReports)
	return FinalReport, nil
}

// displayWeeklyTeamReport generates report on users who submit standups
func (r *Reporter) displayWeeklyTeamReport() (FinalReport string, e error) {
	localizer := i18n.NewLocalizer(r.bot.Bundle, r.bot.CP.Language)

	var allReports []slack.Attachment

	channels, err := r.bot.DB.GetAllChannels()
	if err != nil {
		logrus.Errorf("GetAllChannels failed: %v", err)
		return FinalReport, err
	}

	reportHeaderWeekly := localizer.MustLocalize(&i18n.LocalizeConfig{
		DefaultMessage: &i18n.Message{
			ID:          "ReportHeaderWeekly",
			Description: "Displays a header of weekly report",
			Other:       "Weekly report",
		},
	})

	for _, channel := range channels {
		var attachmentsPull []model.AttachmentItem
		var attachments []slack.Attachment

		channelMembers, err := r.bot.DB.ListChannelMembers(channel.ChannelID)
		if err != nil {
			logrus.Errorf("ListChannelMembers failed for channel %v: %v", channel.ChannelName, err)
			continue
		}

		if len(channelMembers) == 0 {
			logrus.Infof("Skip %v channel", channel.ChannelID)
			continue
		}

		for _, member := range channelMembers {
			var attachment slack.Attachment
			var attachmentFields []slack.AttachmentField
			var worklogs, commits string
			var worklogsPoints, commitsPoints int

			UserInfo, err := r.bot.DB.SelectUser(member.UserID)
			if err != nil {
				logrus.Errorf("SelectUser failed for  user %v: %v", UserInfo.UserName, err)
				continue
			}

			dataOnUser, dataOnUserInProject, collectorError := r.GetCollectorDataOnMember(member, time.Now().AddDate(0, 0, -7), time.Now().AddDate(0, 0, -1))

			if collectorError == nil {
				worklogs, worklogsPoints = r.processWeeklyWorklogs(dataOnUser.Worklogs, dataOnUserInProject.Worklogs)
				commits, commitsPoints = r.processCommits(dataOnUser.Commits, dataOnUserInProject.Commits)
			}

			if member.RoleInChannel == "pm" || member.RoleInChannel == "designer" {
				commits = ""
				commitsPoints++
			}

			if r.bot.CP.CollectorEnabled == false || collectorError != nil {
				worklogs = ""
				worklogsPoints++
				commits = ""
				commitsPoints++
			}

			fieldValue := worklogs + commits

			//if there is nothing to show, do not create attachment
			if fieldValue == "" {
				continue
			}

			attachmentFields = append(attachmentFields, slack.AttachmentField{
				Value: fieldValue,
				Short: false,
			})

			points := worklogsPoints + commitsPoints

			//attachment text will be depend on worklogsPoints and commitsPoints
			if points >= 2 {
				notTagStanduper := localizer.MustLocalize(&i18n.LocalizeConfig{
					DefaultMessage: &i18n.Message{
						ID:          "NotTagStanduper",
						Description: "Displays a message without a user tag",
						Other:       "{{.user}} in #{{.channel}}",
					},
					TemplateData: map[string]interface{}{
						"user":    UserInfo.RealName,
						"channel": channel.ChannelName,
					},
				})
				attachment.Text = notTagStanduper
			} else {
				tagStanduper := localizer.MustLocalize(&i18n.LocalizeConfig{
					DefaultMessage: &i18n.Message{
						ID:          "TagStanduper",
						Description: "Displays a message with a user tag",
						Other:       "<@{{.user}}> in #{{.channel}}",
					},
					TemplateData: map[string]interface{}{
						"user":    member.UserID,
						"channel": channel.ChannelName,
					},
				})
				attachment.Text = tagStanduper
			}

			switch points {
			case 0:
				attachment.Color = "danger"
			case 1:
				attachment.Color = "warning"
			case 2:
				attachment.Color = "good"
			}

			attachment.Fields = attachmentFields

			item := model.AttachmentItem{
				SlackAttachment: attachment,
				Points:          dataOnUserInProject.Worklogs,
			}

			attachmentsPull = append(attachmentsPull, item)
		}

		if len(attachmentsPull) == 0 {
			continue
		}

		attachments = r.sortReportEntries(attachmentsPull)

		r.bot.SendMessage(channel.ChannelID, reportHeaderWeekly, attachments)

		allReports = append(allReports, attachments...)
	}

	if len(allReports) == 0 {
		return
	}

	r.bot.SendMessage(r.bot.CP.ReportingChannel, reportHeaderWeekly, allReports)
	FinalReport = fmt.Sprintf(reportHeaderWeekly, allReports)
	return FinalReport, nil
}

func (r *Reporter) processWorklogs(totalWorklogs, projectWorklogs int) (string, int) {
	localizer := i18n.NewLocalizer(r.bot.Bundle, r.bot.CP.Language)

	points := 0
	worklogsEmoji := ""

	w := totalWorklogs / 3600
	switch {
	case w < 3:
		worklogsEmoji = ":angry:"
	case w >= 3 && w < 7:
		worklogsEmoji = ":disappointed:"
	case w >= 7 && w < 9:
		worklogsEmoji = ":wink:"
		points++
	case w >= 9:
		worklogsEmoji = ":sunglasses:"
		points++
	}
	worklogsTime := utils.SecondsToHuman(totalWorklogs)

	if totalWorklogs != projectWorklogs {
		worklogsTimeTranslation := localizer.MustLocalize(&i18n.LocalizeConfig{
			DefaultMessage: &i18n.Message{
				ID:          "WorklogsTimeTranslation",
				Description: "Displays message about worklogs time",
				Other:       "{{.projectWorklogs}} out of {{.totalWorklogs}}",
			},
			TemplateData: map[string]interface{}{
				"projectWorklogs": utils.SecondsToHuman(projectWorklogs),
				"totalWorklogs":   utils.SecondsToHuman(totalWorklogs),
			},
		})
		worklogsTime = worklogsTimeTranslation
	}

	if int(time.Now().Weekday()) == 0 || int(time.Now().Weekday()) == 1 {
		worklogsEmoji = ""
		if projectWorklogs == 0 {
			return "", points
		}
	}

	worklogsTranslation := localizer.MustLocalize(&i18n.LocalizeConfig{
		DefaultMessage: &i18n.Message{
			ID:          "WorklogsTranslation",
			Description: "Displays message about worklogs",
			Other:       " worklogs: {{.worklogsTime}} {{.worklogsEmoji}} |",
		},
		TemplateData: map[string]interface{}{
			"worklogsTime":  worklogsTime,
			"worklogsEmoji": worklogsEmoji,
		},
	})
	worklogs := worklogsTranslation
	return worklogs, points
}

func (r *Reporter) processWeeklyWorklogs(totalWorklogs, projectWorklogs int) (string, int) {
	localizer := i18n.NewLocalizer(r.bot.Bundle, r.bot.CP.Language)

	points := 0
	worklogsEmoji := ""

	w := totalWorklogs / 3600
	switch {
	case w < 31:
		worklogsEmoji = ":disappointed:"
	case w >= 31 && w < 35:
		worklogsEmoji = ":wink:"
		points++
	case w >= 35:
		worklogsEmoji = ":sunglasses:"
		points++
	}
	worklogsTime := utils.SecondsToHuman(totalWorklogs)

	if totalWorklogs != projectWorklogs {
		worklogsTimeTranslation := localizer.MustLocalize(&i18n.LocalizeConfig{
			DefaultMessage: &i18n.Message{
				ID:          "WorklogsTimeTranslation",
				Description: "Displays message about worklogs time",
				Other:       "{{.projectWorklogs}} out of {{.totalWorklogs}}",
			},
			TemplateData: map[string]interface{}{
				"projectWorklogs": utils.SecondsToHuman(projectWorklogs),
				"totalWorklogs":   utils.SecondsToHuman(totalWorklogs),
			},
		})
		worklogsTime = worklogsTimeTranslation
	}

	worklogsTranslation := localizer.MustLocalize(&i18n.LocalizeConfig{
		DefaultMessage: &i18n.Message{
			ID:          "WorklogsTranslation",
			Description: "Displays message about worklogs",
			Other:       " worklogs: {{.worklogsTime}} {{.worklogsEmoji}} |",
		},
		TemplateData: map[string]interface{}{
			"worklogsTime":  worklogsTime,
			"worklogsEmoji": worklogsEmoji,
		},
	})
	worklogs := worklogsTranslation
	return worklogs, points
}

func (r *Reporter) processCommits(totalCommits, projectCommits int) (string, int) {
	localizer := i18n.NewLocalizer(r.bot.Bundle, r.bot.CP.Language)

	points := 0
	commitsEmoji := ""

	c := projectCommits
	switch {
	case c == 0:
		commitsEmoji = ":shit:"
	case c > 0:
		commitsEmoji = ":wink:"
		points++
	}

	if int(time.Now().Weekday()) == 0 || int(time.Now().Weekday()) == 1 {
		commitsEmoji = ""
		if projectCommits == 0 {
			return "", points
		}
	}

	commitsTranslation := localizer.MustLocalize(&i18n.LocalizeConfig{
		DefaultMessage: &i18n.Message{
			ID:          "CommitsTranslation",
			Description: "Displays message about commits",
			Other:       " commits: {{.projectCommits}} {{.commitsEmoji}} |",
		},
		TemplateData: map[string]interface{}{
			"projectCommits": projectCommits,
			"commitsEmoji":   commitsEmoji,
		},
	})

	commits := commitsTranslation
	return commits, points
}

func (r *Reporter) processStandup(member model.ChannelMember) (string, int) {
	localizer := i18n.NewLocalizer(r.bot.Bundle, r.bot.CP.Language)

	points := 0
	standup := ""
	t := time.Now().AddDate(0, 0, -1)

	shouldBeTracked := r.bot.DB.MemberShouldBeTracked(member.ID, t)
	if !shouldBeTracked {
		points++
		return "", points
	}

	timeFrom := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	timeTo := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.Local)

	isNonReporter, err := r.bot.DB.IsNonReporter(member.UserID, member.ChannelID, timeFrom, timeTo)

	if err != nil {
		points++
		return "", points
	}

	if isNonReporter == true {
		noStandup := localizer.MustLocalize(&i18n.LocalizeConfig{
			DefaultMessage: &i18n.Message{
				ID:          "NoStandup",
				Description: "Displays message if standuper hasn't standup",
				Other:       " standup :x: ",
			},
		})
		standup = noStandup
	} else {
		hasStandup := localizer.MustLocalize(&i18n.LocalizeConfig{
			DefaultMessage: &i18n.Message{
				ID:          "HasStandup",
				Description: "Displays message if standuper has standup",
				Other:       " standup :heavy_check_mark: ",
			},
		})
		standup = hasStandup
		points++
	}

	return standup, points
}

func (r *Reporter) sortReportEntries(entries []model.AttachmentItem) []slack.Attachment {
	var attachments []slack.Attachment

	for i := 0; i < len(entries); i++ {
		if !sweep(entries, i) {
			break
		}
	}

	for _, item := range entries {
		attachments = append(attachments, item.SlackAttachment)
	}

	return attachments
}

func sweep(entries []model.AttachmentItem, prevPasses int) bool {
	var N = len(entries)
	var didSwap = false
	var firstIndex = 0
	var secondIndex = 1

	for secondIndex < (N - prevPasses) {

		var firstItem = entries[firstIndex]
		var secondItem = entries[secondIndex]
		if entries[firstIndex].Points < entries[secondIndex].Points {
			entries[firstIndex] = secondItem
			entries[secondIndex] = firstItem
			didSwap = true
		}
		firstIndex++
		secondIndex++
	}

	return didSwap
}

//GetCollectorDataOnMember sends API request to Collector endpoint and returns CollectorData type
func (r *Reporter) GetCollectorDataOnMember(member model.ChannelMember, startDate, endDate time.Time) (collector.Data, collector.Data, error) {
	dateFrom := fmt.Sprintf("%d-%02d-%02d", startDate.Year(), startDate.Month(), startDate.Day())
	dateTo := fmt.Sprintf("%d-%02d-%02d", endDate.Year(), endDate.Month(), endDate.Day())

	project, err := r.bot.DB.GetChannelName(member.ChannelID)
	if err != nil {
		return collector.Data{}, collector.Data{}, err
	}

	dataOnUser, err := collector.GetCollectorData(r.bot, "users", member.UserID, dateFrom, dateTo)
	if err != nil {
		return collector.Data{}, collector.Data{}, err
	}

	userInProject := fmt.Sprintf("%v/%v", member.UserID, project)
	dataOnUserInProject, err := collector.GetCollectorData(r.bot, "user-in-project", userInProject, dateFrom, dateTo)
	if err != nil {
		return collector.Data{}, collector.Data{}, err
	}

	return dataOnUser, dataOnUserInProject, err
}

func formatTime(t string) (hour, min int, err error) {
	var er = errors.New("time format error")
	ts := strings.Split(t, ":")
	if len(ts) != 2 {
		err = er
		return
	}

	hour, err = strconv.Atoi(ts[0])
	if err != nil {
		return
	}
	min, err = strconv.Atoi(ts[1])
	if err != nil {
		return
	}

	if hour < 0 || hour > 23 || min < 0 || min > 59 {
		err = er
		return
	}
	return hour, min, nil
}
