package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/caarlos0/env"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/alertmanager/template"
)

type config struct {
	ExpireDuration      time.Duration `env:"EXPIRE_DURATION" envDefault:"30m"`
	InternalChkInterval time.Duration `env:"INTERNAL_CHK_INTERVAL" envDefault:"1m"`
	Debug               bool          `env:"DEBUG"`
	Port                int           `env:"PORT" envDefault:"8080"`
	SlackURL            string        `env:"SLACK_URL"`
	SlackChannel        string        `env:"SLACK_CHANNEL" envDefault:"#general"`
	SlackUsername       string        `env:"SLACK_USERNAME" envDefault:"Watchdog Monitor"`
	SlackIcon           string        `env:"SLACK_ICON" envDefault:":monkey_face:"`
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

type watchedAlerts struct {
	Alerts map[string]*watchedAlert
}

func (wAlerts *watchedAlerts) checkAlertsExpiry() {
	for _, wAlert := range wAlerts.Alerts {
		fp := wAlert.Alert.Fingerprint
		if wAlert.expired() {
			log.Printf("Alert %s has expired and is now considered missing, raising an alarm !", fp)
			// Send signal & create a notification !!
			delete(wAlerts.Alerts, fp)
		}
	}
}

var (
	knownAlerts = watchedAlerts{
		Alerts: map[string]*watchedAlert{},
	}
	conf = config{}
)

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
	r := setupRouter()
	go expiryCheckerRoutine(conf.InternalChkInterval)

	err := r.Run(fmt.Sprintf(":%d", conf.Port))
	if err != nil {
		log.Fatalf("Could not start webserver: %s", err.Error())
	}
}
