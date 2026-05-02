import { api } from "@/lib/api-client";
import type {
  AuthMethods,
  AuthSession,
  LoginResponse,
  UserProfile,
} from "@/types/api";
import type { LoginInput } from "./schemas";

export async function getAuthMethods(): Promise<AuthMethods> {
  return api<AuthMethods>("/api/auth/methods");
}

export async function login(input: LoginInput): Promise<LoginResponse> {
  return api<LoginResponse>("/api/auth/login", { method: "POST", body: input });
}

export async function getSession(): Promise<AuthSession> {
  return api<AuthSession>("/api/auth/session");
}

export async function getProfile(): Promise<UserProfile> {
  return api<UserProfile>("/api/users/profile");
}
