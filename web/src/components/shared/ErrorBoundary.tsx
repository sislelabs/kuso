"use client";

import { Component, type ReactNode } from "react";

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
}
interface State {
  error: Error | null;
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: unknown) {
    console.error("ErrorBoundary caught:", error, info);
  }

  render() {
    if (this.state.error) {
      return (
        this.props.fallback ?? (
          <div className="p-8 text-center text-sm text-[var(--text-secondary)]">
            <p className="font-mono">something broke</p>
            <p className="mt-2 text-xs">{this.state.error.message}</p>
          </div>
        )
      );
    }
    return this.props.children;
  }
}
