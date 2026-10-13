package services

// This file has been modified for the KestrelDesk project.

import "github.com/Swhshd/kestreldesk/internal/models"

var TriggerAIReplyAsyncHook func(conversation models.Conversation, message models.Message)
