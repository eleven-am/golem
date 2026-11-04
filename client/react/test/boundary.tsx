import { Component, type ReactNode } from 'react'

interface State {
  readonly error: Error | undefined
}

export class Boundary extends Component<{ readonly children?: ReactNode }, State> {
  state: State = { error: undefined }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  render() {
    return this.state.error === undefined ? this.props.children : <p data-testid="caught">{`${this.state.error.name}: ${this.state.error.message}`}</p>
  }
}
