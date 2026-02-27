import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "@/components/auth-provider";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
} from "@/components/ui/card";

export function LoginPage() {
  const [token, setToken] = useState("");
  const { login } = useAuth();
  const navigate = useNavigate();

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (token.trim()) {
      login(token.trim());
      navigate("/dashboard");
    }
  };

  return (
    <div className="relative flex min-h-screen items-center justify-center bg-background overflow-hidden grid-pattern">
      {/* Background gradient orbs matching website */}
      <div className="absolute top-1/4 left-1/4 w-96 h-96 bg-indigo-500/10 rounded-full blur-[120px]" />
      <div className="absolute bottom-1/4 right-1/4 w-96 h-96 bg-purple-500/10 rounded-full blur-[120px]" />
      <div className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-[400px] h-[400px] bg-orange-500/5 rounded-full blur-[150px]" />

      <Card className="relative z-10 w-full max-w-md border-border/50 bg-card/80 backdrop-blur-xl shadow-2xl shadow-black/20">
        <CardHeader className="text-center">
          <div className="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-xl bg-gradient-to-br from-indigo-500 to-purple-600 text-white font-bold text-2xl shadow-lg shadow-indigo-500/25">
            S
          </div>
          <CardTitle className="text-2xl font-bold text-white">
            Sympo<span className="text-orange-500">zium</span>
          </CardTitle>
          <CardDescription>
            Enter your API token to access the dashboard
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="token">API Token</Label>
              <Input
                id="token"
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="Enter your bearer token"
                autoFocus
              />
            </div>
            <Button type="submit" className="w-full bg-gradient-to-r from-indigo-500 to-purple-600 hover:from-indigo-600 hover:to-purple-700 text-white border-0 shadow-lg shadow-indigo-500/20" disabled={!token.trim()}>
              Sign In
            </Button>
            <p className="text-center text-xs text-muted-foreground">
              Provide the token used with{" "}
              <code className="rounded bg-muted px-1 py-0.5 font-mono">
                sympozium serve --token
              </code>
            </p>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
