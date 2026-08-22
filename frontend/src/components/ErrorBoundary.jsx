import { Component } from 'react'
import { AlertCircle, RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

// React only surfaces render errors to a class component, so this stays a class
// even though nothing else in the app is one.
//
// Without a boundary anywhere, one throw in one page unmounted the whole admin
// UI and left a blank document — no sidebar, no way back, and nothing on screen
// saying what happened. A boundary per page keeps the failure to the page that
// caused it.
class ErrorBoundary extends Component {
  constructor(props) {
    super(props)
    this.state = { error: null }
  }

  static getDerivedStateFromError(error) {
    return { error }
  }

  componentDidCatch(error, info) {
    // The stack is worth more than the message on its own, and the browser
    // console is the only place a user can be asked to look.
    console.error(`Unhandled error in ${this.props.label || 'the interface'}:`, error, info)
  }

  handleRetry = () => {
    this.setState({ error: null })
  }

  render() {
    const { error } = this.state
    if (!error) return this.props.children

    const { label, onReload } = this.props
    return (
      <div className="flex flex-1 items-start justify-center p-4 lg:p-5">
        <Card className="w-full max-w-xl border-destructive/40">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-xl">
              <AlertCircle className="h-5 w-5 text-destructive" />
              Something broke {label ? `in ${label}` : 'here'}
            </CardTitle>
            <CardDescription>
              The rest of StreamNZB is still running — this page stopped, not the server.
              Streams already playing are unaffected.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex items-start gap-2 rounded-md bg-destructive/10 p-3 text-sm text-destructive">
              <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
              <span className="break-words">{error.message || String(error)}</span>
            </div>
            <p className="text-sm text-muted-foreground">
              Full details are in the browser console. If this keeps happening, include them in a
              bug report.
            </p>
            <div className="flex flex-wrap gap-2">
              <Button onClick={this.handleRetry} variant="default">
                <RotateCcw className="mr-2 h-4 w-4" />
                Try again
              </Button>
              <Button onClick={onReload || (() => window.location.reload())} variant="outline">
                Reload the page
              </Button>
            </div>
          </CardContent>
        </Card>
      </div>
    )
  }
}

export default ErrorBoundary
