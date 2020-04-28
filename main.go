package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/PagerDuty/go-pagerduty"
	"github.com/caarlos0/env"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/alertmanager/template"
	"github.com/slack-go/slack"
)

var (
	knownAlerts = watchedAlerts{
		Alerts: map[string]*watchedAlert{},
	}
	conf     = config{}
	slackAPI = &slack.Client{}
)

type config struct {
	ExpireDuration      time.Duration `env:"EXPIRE_DURATION" envDefault:"1h"`
	InternalChkInterval time.Duration `env:"INTERNAL_CHK_INTERVAL" envDefault:"1m"`
	Debug               bool          `env:"DEBUG"`
	Port                int           `env:"PORT" envDefault:"8080"`
	PagerdutyToken      string        `env:"PD_TOKEN" envDefault:""`
	SlackToken          string        `env:"SLACK_TOKEN" envDefault:""`
	SlackChannel        string        `env:"SLACK_CHANNEL" envDefault:"general"`
	SlackChannelID      string
}

type watchedAlert struct {
	Alert    template.Alert
	ExpireAt time.Time
}

func (wa *watchedAlert) resetExpiryDate() {
	wa.ExpireAt = time.Now().Add(conf.ExpireDuration)
}

func (wa *watchedAlert) expired() bool {
	return time.Now().After(wa.ExpireAt)
}

func (wa *watchedAlert) sendSlackNotification() {
	prettyAlert, err := json.MarshalIndent(wa.Alert, "", "    ")
	if err != nil {
		log.Fatalf("Impossible to format alert %s as json string: %s", wa.Alert.Fingerprint, err.Error())
	}

	labels := ""
	for i, labelName := range wa.Alert.Labels.SortedPairs().Names() {
		labelValue := wa.Alert.Labels.SortedPairs().Values()[i]
		labels = labels + "- " + fmt.Sprintf("%s = %s", labelName, labelValue) + "\n"
	}

	msgHeaderText := slack.NewTextBlockObject(
		slack.MarkdownType,
		":skull: *This is an alert !* :skull: \nA watchdog alert has not been refreshed for too long.\nPlease check monitoring stack status.",
		false,
		false,
	)

	msgContentText := slack.NewTextBlockObject(
		slack.MarkdownType,
		"*Lost watchdog labels:*\n```"+labels+"```",
		false,
		false,
	)

	msgHeader := slack.NewSectionBlock(msgHeaderText, nil, nil)
	msgContent := slack.NewSectionBlock(msgContentText, nil, nil)

	msgAttachment := slack.Attachment{
		Title: "Lost alert full description",
		Text:  "```" + string(prettyAlert) + "```",
		Color: "#a10606",
	}
	_, _, _, err = slackAPI.SendMessage(
		conf.SlackChannelID,
		slack.MsgOptionIconEmoji(":skull:"),
		slack.MsgOptionBlocks(msgHeader, msgContent),
		slack.MsgOptionAttachments(msgAttachment),
	)
	if err != nil {
		fmt.Printf("Error sending alert %s slack notification: %s", wa.Alert.Fingerprint, err.Error())
	}
}

func (wa *watchedAlert) sendPagerdutyNotification() {
	prettyAlert, err := json.MarshalIndent(wa.Alert, "", "    ")
	if err != nil {
		log.Fatalf("Impossible to format alert %s as json string: %s", wa.Alert.Fingerprint, err.Error())
	}

	labels := "A MONITORED WATCHDOG ALERT IS MISSING !\n\nAlert labels:\n"
	for i, labelName := range wa.Alert.Labels.SortedPairs().Names() {
		labelValue := wa.Alert.Labels.SortedPairs().Values()[i]
		labels = labels + fmt.Sprintf("%s = %s", labelName, labelValue) + "\n"
	}

	message := labels + "\n" + string(prettyAlert)

	event := pagerduty.Event{
		Type:        "trigger",
		ServiceKey:  conf.PagerdutyToken,
		Description: "Watchdog monitored alert is missing for too long",
		Details:     message,
	}

	_, err = pagerduty.CreateEvent(event)
	if err != nil {
		log.Printf("Error sending alert %s as pagerduty event: %s", wa.Alert.Fingerprint, err.Error())
	}
}

type watchedAlerts struct {
	Alerts map[string]*watchedAlert
}

func (was watchedAlerts) checkAlertsExpiry() {
	for id, alert := range knownAlerts.Alerts {
		if alert.expired() {
			log.Printf("Alert %s has expired and is now considered missing, triggering notifiers", id)
			if conf.SlackToken != "" {
				alert.sendSlackNotification()
			}
			if conf.PagerdutyToken != "" {
				alert.sendPagerdutyNotification()
			}
			delete(knownAlerts.Alerts, id)
		}
	}
}

func webhookHandler(c *gin.Context) {
	// Only accept well formated json using alertmanager own format
	var webhookData template.Data
	if c.Bind(&webhookData) != nil {
		log.Println("Webhook payload format invalid. Skipping event")
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "reason": "payload-format-error"})
		return
	}

	// Data Validation
	// Don't process Alertmanager webhook events that are not firing events
	if webhookData.Status != "firing" {
		log.Println("Received webhook status is not firing. Skipping event")
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "reason": "webhook-not-firing"})
		return
	}

	// Processing alerts
	// There might be multiple alerts included in the webhook event
	for _, alert := range webhookData.Alerts {
		fp := alert.Fingerprint

		if wdAlert, ok := knownAlerts.Alerts[fp]; ok {
			// If the alert is already known, just reset the expiry date
			log.Printf("Refreshing alert %s expiry", fp)
			wdAlert.resetExpiryDate()
		} else {
			// else, register the new alert
			log.Printf("Registering new alert %s", fp)
			expiry := time.Now().Add(conf.ExpireDuration)
			knownAlerts.Alerts[fp] = &watchedAlert{
				Alert:    alert,
				ExpireAt: expiry,
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func setupRouter() *gin.Engine {
	gin.DisableConsoleColor()
	if conf.Debug {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())
	log.SetPrefix("[DEADMAN] ")

	r.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	r.POST("/webhook", webhookHandler)

	return r
}

func expiryCheckerRoutine(interval time.Duration) {
	checkExpiryTicker := time.NewTicker(interval)
	for {
		<-checkExpiryTicker.C
		knownAlerts.checkAlertsExpiry()
	}
}

func setupSlackNotifier() {
	if conf.SlackToken != "" {
		slackAPI = slack.New(conf.SlackToken)

		_, err := slackAPI.AuthTest()
		if err != nil {
			log.Fatalf("Impossible to initialize Slack client: %s", err.Error())
		}

		// TODO: support paginated channels list
		channels, _ := slackAPI.GetChannels(false)
		for _, channel := range channels {
			if strings.EqualFold(channel.Name, conf.SlackChannel) {
				conf.SlackChannelID = channel.ID
			}
		}

		if conf.SlackChannelID == "" {
			log.Fatalf("Impossible to find target Slack channel: %s", conf.SlackChannel)
		}

		log.Printf("Slack notifier initialized on channel %s (%s)", conf.SlackChannel, conf.SlackChannelID)
	}
}

func setupPagerdutyNotifier() {
	if conf.PagerdutyToken != "" {
		// There's nothing to do.
		// It seems that we don't need any global session when using a PD service token API.
		log.Println("Pagerduty notifier initialized")
	}
}

func setupNotifiers() {
	setupSlackNotifier()
	setupPagerdutyNotifier()
}

func printConfig() {
	log.Println("Starting Alertmanager Deadman Receiver")
	log.Printf("Any missing alert is notified after %s not firing", conf.ExpireDuration)
	log.Printf("Internal check routine interval is %s", conf.InternalChkInterval)
	log.Printf("Listening on port %d", conf.Port)
}

func main() {
	if err := env.Parse(&conf); err != nil {
		log.Fatal("Unable to parse envs: ", err)
	}

	printConfig()
	setupNotifiers()
	r := setupRouter()
	go expiryCheckerRoutine(conf.InternalChkInterval)

	err := r.Run(fmt.Sprintf(":%d", conf.Port))
	if err != nil {
		log.Fatalf("Could not start webserver: %s", err.Error())
	}
}
