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

type watchdogAlert struct {
	Alert    template.Alert
	ExpireAt time.Time
}

func (wa *watchdogAlert) resetExpiryDate() {
	wa.ExpireAt = time.Now().Add(conf.ExpireDuration)
}

func (wa *watchdogAlert) expired() bool {
	return time.Now().After(wa.ExpireAt)
}

type watchdogAlerts struct {
	Alerts map[string]*watchdogAlert
}

func (wdAlerts *watchdogAlerts) checkAlertsExpiry() {
	for _, wdAlert := range wdAlerts.Alerts {
		fp := wdAlert.Alert.Fingerprint
		if wdAlert.expired() {
			log.Printf("Watchdog %s has expired, raising a watchdog alarm !", fp)
			// Send signal & create a notification !!
			delete(wdAlerts.Alerts, fp)
		}
	}
}

var (
	receivedAlerts = watchdogAlerts{
		Alerts: map[string]*watchdogAlert{},
	}
	conf = config{}
)

func watchdogHandler(c *gin.Context) {
	// Only accept well formated json using alertmanager own format
	var alertsData template.Data
	if c.Bind(&alertsData) != nil {
		log.Println("Payload format is not valid. Skipping event")
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "reason": "payload-format-error"})
		return
	}

	// Data Validation
	// Don't process Alertmanager webhook events that are not firing events
	if alertsData.Status != "firing" {
		log.Println("Received webhook event status is not firing. Skipping event")
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "reason": "webhook-not-firing"})
		return
	}

	// Processing alerts
	// There might be multiple alerts included in the webhook event
	for _, alert := range alertsData.Alerts {
		fp := alert.Fingerprint

		if wdAlert, ok := receivedAlerts.Alerts[fp]; ok {
			// If the alert is already known, just reset the expiry date
			log.Printf("Refreshing alert %s expiry", fp)
			wdAlert.resetExpiryDate()
		} else {
			// else, register the new alert
			log.Printf("Registering new alert %s", fp)
			expiry := time.Now().Add(conf.ExpireDuration)
			receivedAlerts.Alerts[fp] = &watchdogAlert{
				Alert:    alert,
				ExpireAt: expiry,
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
	return
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
	log.SetPrefix("[WATCHDOG] ")

	r.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	r.POST("/watchdog", watchdogHandler)

	return r
}

func watchdogRoutine(interval time.Duration) {
	checkExpiryTicker := time.NewTicker(interval)
	for {
		select {
		case <-checkExpiryTicker.C:
			receivedAlerts.checkAlertsExpiry()
		}
	}
}

func printConfig() {
	log.Println("Starting Alertmanager Watchdog Receiver")
	log.Printf("Missing watchdog is notified after %s", conf.ExpireDuration)
	log.Printf("Internal check routine period is %s", conf.InternalChkInterval)
	log.Printf("Listening on port %d", conf.Port)
}

func main() {
	if err := env.Parse(&conf); err != nil {
		log.Fatal("Unable to parse envs: ", err)
	}

	r := setupRouter()
	printConfig()
	go watchdogRoutine(conf.InternalChkInterval)

	r.Run(fmt.Sprintf(":%d", conf.Port))
}
