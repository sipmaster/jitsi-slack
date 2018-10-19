package jitsi

import (
	"fmt"

	"github.com/nlopes/slack"
)

const (
	roomTemplate = `{
		"response_type":"in_channel",
		"attachments":[{
			"fallback":"Meeting started on %s",
			"title":"Meeting started on %s",
			"color":"#3AA3E3",
			"attachment_type":"default",
			"actions":[{
				"name":"join",
				"text":"Join",
				"type":"button",
				"url":"%s",
				"style":"primary"
			}]
		}]
	}`
	userTemplate = `{
		"response_type":"ephemeral",
		"attachments":[{
			"fallback":"Invitations have been sent for your meeting on %s",
			"title":"Invitations have been sent for your meeting on %s",
			"color":"#3AA3E3",
			"attachment_type":"default",
			"actions":[{
				"name":"join",
				"text":"Join",
				"type":"button",
				"url":"%s",
				"style":"primary"
			}]
		}]
	}`
	helpMessage = `{
		"response_type":"ephemeral",
		"text":"How to use /jitsi...",
		"attachments":[{
			"text":"To share a conference link with the channel, use '/jitsi'. Now everyone can join.\nTo share a conference link with users, use 'jitsi @bob @alice'. Now you can meet with Bob and Alice."
		}]
	}`
	installMessage = `{
		"response_type":"ephemeral",
		"text":"Please install the jitsi meet app to integrate with your slack workspace.",
		"attachments":[{"text":"%s"}]
	}`
)

func sendPersonalizedInvite(token, hostID, userID string, meeting *Meeting) error {
	slackClient := slack.New(token)
	userInfo, err := slackClient.GetUserInfo(userID)
	if err != nil {
		return err
	}

	msg := fmt.Sprintf(
		"<@%s> would like you to join a meeting on %s",
		hostID,
		meeting.Host,
	)

	meetingURL, err := meeting.AuthenticatedURL(
		userInfo.ID,
		userInfo.Name,
		userInfo.Profile.Image192,
	)
	if err != nil {
		return err
	}

	params := slack.PostMessageParameters{
		Attachments: []slack.Attachment{
			slack.Attachment{
				Fallback: msg,
				Title:    msg,
				Color:    "#3AA3E3",
				Actions: []slack.AttachmentAction{
					slack.AttachmentAction{
						Name:  "join",
						Text:  "Join",
						Type:  "button",
						Style: "primary",
						URL:   meetingURL,
					},
				},
			},
		},
	}

	channel, _, _, err := slackClient.OpenConversation(
		&slack.OpenConversationParameters{
			Users: []string{userID},
		},
	)
	if err != nil {
		return err
	}

	_, _, err = slackClient.PostMessage(channel.ID, "", params)
	return err
}

func joinPersonalMeetingMsg(token, userID string, meeting *Meeting) (string, error) {
	slackClient := slack.New(token)
	userInfo, err := slackClient.GetUserInfo(userID)
	if err != nil {
		return "", err
	}

	meetingURL, err := meeting.AuthenticatedURL(
		userInfo.ID,
		userInfo.Name,
		userInfo.Profile.Image192,
	)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(userTemplate, meeting.Host, meeting.Host, meetingURL), nil
}
