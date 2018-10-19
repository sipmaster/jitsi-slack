package jitsi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/rs/zerolog/hlog"
)

const (
	// error strings from slack api
	errInvalidAuth      = "invalid_auth"
	errInactiveAccount  = "account_inactive"
	errMissingAuthToken = "not_authed"
)

var (
	atMentionRE    = regexp.MustCompile(`<@([^>|]+)`)
	serverCmdRE    = regexp.MustCompile(`^server`)
	serverConfigRE = regexp.MustCompile(`^server\s+(<https?:\/\/\S+>)`)
	helpCmdRE      = regexp.MustCompile(`^help`)
)

// TokenReader provides an interface for reading access token data from
// a token store.
type TokenReader interface {
	GetFirstBotTokenForTeam(teamID string) (string, error)
}

// ServerConfigWriter provides an interface for writing server configuration
// data for a team's workspace.
type ServerConfigWriter interface {
	Store(*ServerCfgData) error
	Remove(string) error
}

func handleRequestValidation(w http.ResponseWriter, r *http.Request, SlackSigningSecret string) bool {
	ts := r.Header.Get(RequestTimestampHeader)
	sig := r.Header.Get(RequestSignatureHeader)
	if ts == "" || sig == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return false
	}
	defer r.Body.Close()

	if !ValidRequest(SlackSigningSecret, string(body), ts, sig) {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}

	r.Body = ioutil.NopCloser(bytes.NewBuffer(body))
	return true
}

func help(w http.ResponseWriter) {
	w.Header().Set("Content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(helpMessage))
}

func install(w http.ResponseWriter, sharableURL string) {
	installMsg := fmt.Sprintf(installMessage, sharableURL)
	w.Header().Set("Content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(installMsg))
}

// SlashCommandHandlers provides http handlers for Slack slash commands
// that integrate with Jitsi Meet.
type SlashCommandHandlers struct {
	MeetingGenerator   *MeetingGenerator
	SlackSigningSecret string
	TokenReader        TokenReader
	SharableURL        string
	ServerConfigWriter ServerConfigWriter
}

// Jitsi will create a conference and dispatch an invite message to both users.
// It is a slash command for Slack.
func (s *SlashCommandHandlers) Jitsi(w http.ResponseWriter, r *http.Request) {
	if !handleRequestValidation(w, r, s.SlackSigningSecret) {
		return
	}
	err := r.ParseForm()
	if err != nil {
		hlog.FromRequest(r).Error().
			Err(err).
			Msg("unable to parse form data")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	text := r.PostFormValue("text")
	if helpCmdRE.MatchString(text) {
		help(w)
	} else if serverCmdRE.MatchString(text) {
		s.configureServer(w, r)
	} else {
		s.dispatchInvites(w, r)
	}
}

func (s *SlashCommandHandlers) configureServer(w http.ResponseWriter, r *http.Request) {
	teamID := r.PostFormValue("team_id")
	text := r.PostFormValue("text")

	// First check if the default is being requested.
	configuration := strings.Split(text, " ")
	if configuration[1] == "default" {
		err := s.ServerConfigWriter.Remove(teamID)
		if err != nil {
			hlog.FromRequest(r).Error().
				Err(err).
				Msg("defaulting server")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Your team's conferences will now be hosted on https://meet.jit.si")
		return
	}

	if !serverConfigRE.MatchString(text) {
		w.Header().Set("Content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "A proper conference host must be provided.")
		return
	}

	host := serverConfigRE.FindAllStringSubmatch(text, -1)[0][1]
	host = strings.Trim(host, "<>")
	err := s.ServerConfigWriter.Store(&ServerCfgData{
		TeamID: teamID,
		Server: host,
	})
	if err != nil {
		hlog.FromRequest(r).Error().
			Err(err).
			Msg("configuring server")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Your team's conferences will now be hosted on %s\nRun `/jitsi server default` if you'd like to continue using https://meet.jit.si", host)
}

func (s *SlashCommandHandlers) dispatchInvites(w http.ResponseWriter, r *http.Request) {
	// Generate the meeting data.
	teamID := r.PostFormValue("team_id")
	teamName := r.PostFormValue("team_domain")
	meeting, err := s.MeetingGenerator.New(teamID, teamName)
	if err != nil {
		hlog.FromRequest(r).Error().
			Err(err).
			Msg("generating meeting")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// If nobody was @-mentioned then just send a generic invite to the channel.
	text := r.PostFormValue("text")
	matches := atMentionRE.FindAllStringSubmatch(text, -1)
	if matches == nil {
		w.Header().Set("Content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := fmt.Sprintf(roomTemplate, meeting.Host, meeting.Host, meeting.URL)
		w.Write([]byte(resp))
		return
	}

	// Grab a oauth token for the slack workspace.
	token, err := s.TokenReader.GetFirstBotTokenForTeam(teamID)
	if err != nil {
		switch err.Error() {
		case errMissingAuthToken:
			install(w, s.SharableURL)
		default:
			hlog.FromRequest(r).Error().
				Err(err).
				Msg("retrieving token")
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}

	// Dispatch a personal invite to each user @-mentioned.
	callerID := r.PostFormValue("user_id")
	for _, match := range matches {
		err = sendPersonalizedInvite(token, callerID, match[1], &meeting)
		if err != nil {
			switch err.Error() {
			case errInvalidAuth, errInactiveAccount, errMissingAuthToken:
				install(w, s.SharableURL)
				return
			default:
				hlog.FromRequest(r).Error().
					Err(err).
					Msg("inviting user")
			}
		}
	}

	// Create a personalized response for the meeting initiator.
	resp, err := joinPersonalMeetingMsg(token, callerID, &meeting)
	if err != nil {
		switch err.Error() {
		case errInvalidAuth, errInactiveAccount, errMissingAuthToken:
			install(w, s.SharableURL)
			return
		default:
			hlog.FromRequest(r).Error().
				Err(err).
				Msg("inviting user")
		}
	}
	w.Header().Set("Content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(resp))
}

// TokenWriter provides an interface to write access token data to the
// token store.
type TokenWriter interface {
	Store(data *TokenData) error
}

// SlackOAuthHandlers is used for handling Slack OAuth validation.
type SlackOAuthHandlers struct {
	AccessURLTemplate string
	ClientID          string
	ClientSecret      string
	AppID             string
	TokenWriter       TokenWriter
}

type botToken struct {
	BotUserID      string `json:"bot_user_id"`
	BotAccessToken string `json:"bot_access_token"`
}

type accessResponse struct {
	OK          bool     `json:"ok"`
	AccessToken string   `json:"access_token"`
	Scope       string   `json:"scope"`
	UserID      string   `json:"user_id"`
	TeamName    string   `json:"team_name"`
	TeamID      string   `json:"team_id"`
	Bot         botToken `json:"bot"`
}

// Auth validates OAuth access tokens.
func (o *SlackOAuthHandlers) Auth(w http.ResponseWriter, r *http.Request) {
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		hlog.FromRequest(r).Error().
			Err(err).
			Msg("parsing query params")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if params["error"] != nil {
		hlog.FromRequest(r).Error().
			Err(err).
			Msg("error response user declined install")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	code := params["code"]
	if len(code) != 1 {
		hlog.FromRequest(r).Error().
			Err(err).
			Msg("code not provided")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// TODO: inject an http client with http logging.
	resp, err := http.Get(fmt.Sprintf(
		o.AccessURLTemplate,
		o.ClientID,
		o.ClientSecret,
		code[0],
	))
	if err != nil {
		hlog.FromRequest(r).Error().
			Err(err).
			Msg("oauth req error")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	var access accessResponse
	if err = json.NewDecoder(resp.Body).Decode(&access); err != nil {
		hlog.FromRequest(r).Error().
			Err(err).
			Msg("unable to decode slack access response")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if !access.OK {
		hlog.FromRequest(r).Error().
			Err(err).
			Msg("access not ok")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	err = o.TokenWriter.Store(&TokenData{
		TeamID:      access.TeamID,
		UserID:      access.UserID,
		BotToken:    access.Bot.BotAccessToken,
		BotUserID:   access.Bot.BotUserID,
		AccessToken: access.AccessToken,
	})
	if err != nil {
		hlog.FromRequest(r).Error().
			Err(err).
			Msg("unable to store token")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	redirect := fmt.Sprintf("https://slack.com/app_redirect?app=%s", o.AppID)
	http.Redirect(w, r, redirect, http.StatusFound)
}
