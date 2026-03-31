import React, { useState, useEffect } from 'react'
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { PasswordInput } from "@/components/ui/password-input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { AlertCircle, Loader2, Save, User } from "lucide-react"
import { cn } from "@/lib/utils"

function EnvOverrideNote({ show }) {
  if (!show) return null
  return (
    <p className="text-xs text-muted-foreground flex items-center gap-1 mt-1">
      <AlertCircle className="h-3.5 w-3 shrink-0" />
      Overwritten by environment variable on restart.
    </p>
  )
}

export function ProfilePage({
  currentUser,
  config,
  sendCommand,
  ws,
  onUsernameChanged,
  onPasswordChanged,
}) {
  const envOverrides = config?.env_overrides ?? []
  const currentUsername = config?.admin_username || 'admin' || currentUser

  const [username, setUsername] = useState(currentUsername)
  const [usernameSaving, setUsernameSaving] = useState(false)
  const [usernameMessage, setUsernameMessage] = useState({ type: '', text: '' })

  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [passwordError, setPasswordError] = useState('')
  const [passwordLoading, setPasswordLoading] = useState(false)

  useEffect(() => {
    setUsername(config?.admin_username || 'admin' || currentUser)
  }, [config?.admin_username, currentUser])

  const handleSaveUsername = async (e) => {
    e.preventDefault()
    setUsernameMessage({ type: '', text: '' })
    const trimmed = (username || '').trim()
    if (!trimmed) {
      setUsernameMessage({ type: 'error', text: 'Username cannot be empty.' })
      return
    }
    if (trimmed === (config?.admin_username || 'admin')) {
      setUsernameMessage({ type: '', text: 'No change.' })
      return
    }
    if (!sendCommand) {
      setUsernameMessage({ type: 'error', text: 'Not connected.' })
      return
    }
    setUsernameSaving(true)
    window.profileUsernameCallback = (payload) => {
      setUsernameSaving(false)
      const ok = payload.status === 'success'
      if (!ok) {
        setUsernameMessage({ type: 'error', text: payload.message || payload.error || 'Save failed.' })
      } else {
        setUsernameMessage({ type: 'success', text: 'Username saved. Use it to log in next time.' })
        onUsernameChanged?.(trimmed)
      }
      delete window.profileUsernameCallback
    }
    sendCommand('save_config', { admin_username: trimmed })
  }

  const handleChangePassword = async (e) => {
    e.preventDefault()
    setPasswordError('')

    if (newPassword !== confirmPassword) {
      setPasswordError('New passwords do not match')
      return
    }
    if (newPassword.length < 6) {
      setPasswordError('Password must be at least 6 characters long')
      return
    }
    if (!sendCommand) {
      setPasswordError('Not connected. Please wait...')
      return
    }

    setPasswordLoading(true)
    try {
      const loginRes = await fetch('/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ username: currentUser, password: currentPassword }),
      })
      const loginData = await loginRes.json()
      if (!loginData.success) {
        setPasswordError('Current password is incorrect')
        setPasswordLoading(false)
        return
      }

      window.passwordChangeCallback = (payload) => {
        setPasswordLoading(false)
        if (payload.error) {
          setPasswordError(payload.error)
        } else {
          setCurrentPassword('')
          setNewPassword('')
          setConfirmPassword('')
          onPasswordChanged?.()
        }
        delete window.passwordChangeCallback
      }
      if (!sendCommand('update_password', { username: currentUser, password: newPassword })) {
        setPasswordError('Failed to send update request')
        setPasswordLoading(false)
        delete window.passwordChangeCallback
      }
    } catch {
      setPasswordError('Failed to connect to server')
      setPasswordLoading(false)
      delete window.passwordChangeCallback
    }
  }

  const usernameDisabled = envOverrides.includes('admin_username')

  return (
    <div className="max-w-5xl space-y-6">
      <div className="flex items-center gap-4">
        <div
          className={cn(
            "flex items-center justify-center rounded-full border border-border bg-muted",
            "h-16 w-16"
          )}
          aria-hidden
        >
          <User className="h-8 w-8 text-muted-foreground" strokeWidth={1.5} />
        </div>
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">Profile</h2>
          <p className="text-sm text-muted-foreground mt-1">
            Change your dashboard login username and password.
          </p>
        </div>
      </div>

      <form onSubmit={handleSaveUsername}>
        <Card>
          <CardHeader>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1 max-w-[30rem] space-y-0.5">
                <CardTitle>Account</CardTitle>
                <CardDescription>Manage the username you use to sign in to the dashboard.</CardDescription>
              </div>
              <Button
                type="submit"
                variant="destructive"
                size="icon"
                className="h-9 w-9"
                disabled={usernameSaving || usernameDisabled}
                aria-label={usernameSaving ? 'Saving username' : 'Save username'}
              >
                {usernameSaving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
              </Button>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="rounded-md border border-border/60">
              <div className="p-3">
                <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                  <div className="min-w-0 xl:flex-1">
                    <Label htmlFor="profile-username" className="text-sm font-medium">Username</Label>
                  </div>
                  <div className="w-full xl:max-w-xs">
                    <Input
                      id="profile-username"
                      value={username}
                      onChange={(e) => setUsername(e.target.value)}
                      placeholder="admin"
                      disabled={usernameSaving || usernameDisabled}
                      className="h-9 w-full"
                    />
                  </div>
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  The username you use to log in. Save to apply.
                </p>
                <EnvOverrideNote show={envOverrides.includes('admin_username')} />
              </div>
            </div>
            {usernameMessage.text && (
              <div className={cn(
                "rounded-md border px-3 py-2 text-sm",
                usernameMessage.type === 'error'
                  ? 'border-destructive/30 bg-destructive/10 text-destructive'
                  : 'border-border bg-muted/30 text-muted-foreground'
              )}>
                {usernameMessage.text}
              </div>
            )}
          </CardContent>
        </Card>
      </form>

      <form onSubmit={handleChangePassword}>
        <Card>
          <CardHeader>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1 max-w-[30rem] space-y-0.5">
                <CardTitle>Password</CardTitle>
                <CardDescription>Change your password. You will stay logged in after changing it.</CardDescription>
              </div>
              <Button
                type="submit"
                variant="destructive"
                size="icon"
                className="h-9 w-9"
                disabled={passwordLoading}
                aria-label={passwordLoading ? 'Saving password' : 'Save password'}
              >
                {passwordLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
              </Button>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="rounded-md border border-border/60">
              <div className="p-3">
                <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                  <div className="min-w-0 xl:flex-1">
                    <Label htmlFor="profile-current-password" className="text-sm font-medium">Current password</Label>
                  </div>
                  <div className="w-full xl:max-w-sm">
                    <PasswordInput
                      id="profile-current-password"
                      placeholder="Enter current password"
                      value={currentPassword}
                      onChange={(e) => setCurrentPassword(e.target.value)}
                      disabled={passwordLoading}
                      className="h-9 w-full"
                    />
                  </div>
                </div>
              </div>
              <div className="relative p-3">
                <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                  <div className="min-w-0 xl:flex-1">
                    <Label htmlFor="profile-new-password" className="text-sm font-medium">New password</Label>
                  </div>
                  <div className="w-full xl:max-w-sm">
                    <PasswordInput
                      id="profile-new-password"
                      placeholder="Min 6 characters"
                      value={newPassword}
                      onChange={(e) => setNewPassword(e.target.value)}
                      disabled={passwordLoading}
                      className="h-9 w-full"
                    />
                  </div>
                </div>
              </div>
              <div className="relative p-3">
                <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                  <div className="min-w-0 xl:flex-1">
                    <Label htmlFor="profile-confirm-password" className="text-sm font-medium">Confirm new password</Label>
                  </div>
                  <div className="w-full xl:max-w-sm">
                    <PasswordInput
                      id="profile-confirm-password"
                      placeholder="Confirm new password"
                      value={confirmPassword}
                      onChange={(e) => setConfirmPassword(e.target.value)}
                      disabled={passwordLoading}
                      className="h-9 w-full"
                    />
                  </div>
                </div>
              </div>
            </div>
            {passwordError && (
              <div className="flex items-center gap-2 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
                <AlertCircle className="h-4 w-4 shrink-0" />
                <span>{passwordError}</span>
              </div>
            )}
          </CardContent>
        </Card>
      </form>
    </div>
  )
}
