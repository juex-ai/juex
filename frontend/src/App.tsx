import { lazy, type ReactNode, Suspense } from "react";
import {
  createBrowserRouter,
  Navigate,
  RouterProvider,
} from "react-router-dom";
import { AppShell } from "@/components/AppShell";
import { LoadingState } from "@/components/LoadingState";

const Fleet = lazy(() =>
  import("@/pages/Fleet").then((module) => ({ default: module.Fleet }))
);
const AgentLogs = lazy(() =>
  import("@/pages/AgentLogs").then((module) => ({
    default: module.AgentLogs,
  }))
);
const AgentConfig = lazy(() =>
  import("@/pages/AgentConfig").then((module) => ({
    default: module.AgentConfig,
  }))
);

const Sessions = lazy(() =>
  import("@/pages/Sessions").then((module) => ({ default: module.Sessions }))
);
const Session = lazy(() =>
  import("@/pages/Session").then((module) => ({ default: module.Session }))
);
const Observables = lazy(() =>
  import("@/pages/Observables").then((module) => ({
    default: module.Observables,
  }))
);
const ObservableDetail = lazy(() =>
  import("@/pages/ObservableDetail").then((module) => ({
    default: module.ObservableDetail,
  }))
);
const History = lazy(() =>
  import("@/pages/History").then((module) => ({ default: module.History }))
);
const Runtime = lazy(() =>
  import("@/pages/Runtime").then((module) => ({ default: module.Runtime }))
);
const RuntimeLayout = lazy(() =>
  import("@/pages/RuntimeLayout").then((module) => ({
    default: module.RuntimeLayout,
  }))
);
const Extensions = lazy(() =>
  import("@/pages/Extensions").then((module) => ({
    default: module.Extensions,
  }))
);

function RouteSuspense({ children }: { children: ReactNode }) {
  return (
    <Suspense fallback={<LoadingState label="Loading page" />}>
      {children}
    </Suspense>
  );
}

const router = createBrowserRouter([
  {
    path: "/",
    element: <AppShell />,
    children: [
      {
        index: true,
        element: null,
      },
      {
        path: "settings",
        element: (
          <RouteSuspense>
            <Fleet />
          </RouteSuspense>
        ),
      },
      {
        path: "agents/:agentId",
        children: [
          {
            index: true,
            element: (
              <RouteSuspense>
                <Sessions />
              </RouteSuspense>
            ),
          },
          {
            path: "sessions/:id",
            element: (
              <RouteSuspense>
                <Session />
              </RouteSuspense>
            ),
          },
          {
            path: "history",
            element: (
              <RouteSuspense>
                <History />
              </RouteSuspense>
            ),
          },
          {
            path: "runtime",
            element: (
              <RouteSuspense>
                <RuntimeLayout />
              </RouteSuspense>
            ),
            children: [
              {
                index: true,
                element: (
                  <RouteSuspense>
                    <Runtime />
                  </RouteSuspense>
                ),
              },
              {
                path: "extensions",
                element: (
                  <RouteSuspense>
                    <Extensions />
                  </RouteSuspense>
                ),
              },
              {
                path: "observables",
                element: (
                  <RouteSuspense>
                    <Observables />
                  </RouteSuspense>
                ),
              },
              {
                path: "observables/:observableId",
                element: (
                  <RouteSuspense>
                    <ObservableDetail />
                  </RouteSuspense>
                ),
              },
              {
                path: "logs",
                element: (
                  <RouteSuspense>
                    <AgentLogs />
                  </RouteSuspense>
                ),
              },
              {
                path: "config",
                element: (
                  <RouteSuspense>
                    <AgentConfig />
                  </RouteSuspense>
                ),
              },
            ],
          },
        ],
      },
      { path: "*", element: <Navigate to="/" replace /> },
    ],
  },
]);

export default function App() {
  return <RouterProvider router={router} />;
}
