// This file has been modified for the KestrelDesk project.

import { clearSession, readSession, writeSession, type AuthSession } from "@/lib/auth"
import { request } from "@/lib/api/client"

export type LoginRequest = {
  username: string
  password: string
}

export type PasswordResetRequest = {
  identifier: string
}

export type PasswordResetSubmission = {
  token: string
  newPassword: string
  confirmPassword: string
}

export type UpdateProfileRequest = {
  avatar: string
  email?: string
  nickname: string
}

export type ProfileAvatarAsset = {
  url: string
}

export function requestPasswordReset(payload: PasswordResetRequest) {
  return request<{ message: string }>("/api/auth/password_reset/request", {
    method: "POST",
    skipAuth: true,
    body: JSON.stringify(payload),
  })
}

export function resetPassword(payload: PasswordResetSubmission) {
  return request<void>("/api/auth/password_reset/reset", {
    method: "POST",
    skipAuth: true,
    body: JSON.stringify(payload),
  })
}

export async function loginWithPassword(payload: LoginRequest) {
  const data = await request<AuthSession>("/api/auth/login", {
    method: "POST",
    body: JSON.stringify(payload),
    skipAuth: true,
  })
  writeSession(data)
  return data
}

export async function exchangeWxWorkTicket(ticket: string) {
  const data = await request<AuthSession>("/api/auth/wxwork_exchange", {
    method: "POST",
    body: JSON.stringify({ ticket }),
    skipAuth: true,
  })
  writeSession(data)
  return data
}

export async function exchangeOIDCTicket(ticket: string) {
  const data = await request<AuthSession>("/api/auth/oidc_exchange", {
    method: "POST",
    body: JSON.stringify({ ticket }),
    skipAuth: true,
  })
  writeSession(data)
  return data
}

export async function fetchProfile() {
  return request<AuthSession>("/api/auth/profile")
}

export async function updateProfile(payload: UpdateProfileRequest) {
  const data = await request<AuthSession>("/api/auth/profile/update", {
    method: "POST",
    body: JSON.stringify(payload),
  })
  const stored = readSession()
  const nextSession: AuthSession = {
    ...data,
    accessToken: data.accessToken || stored?.accessToken || "",
    expiresAt: data.expiresAt || stored?.expiresAt,
  }
  writeSession(nextSession)
  return nextSession
}

export function uploadProfileAvatar(file: File) {
  const formData = new FormData()
  formData.set("file", file)
  return request<ProfileAvatarAsset>("/api/auth/profile/avatar/upload", {
    method: "POST",
    body: formData,
  })
}

export async function logout() {
  try {
    await request("/api/auth/logout", {
      method: "POST",
    })
  } finally {
    clearSession()
  }
}
