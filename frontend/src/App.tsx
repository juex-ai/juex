import { lazy, type ReactNode, Suspense } from "react";
import {
  createBrowserRouter,
  Navigate,
  RouterProvider,
  useParams,
} from "react-router-dom";
import { AppShell } from "@/components/AppShell";

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

function RouteSuspense({ children }: { children: ReactNode }) {
  return (
    <Suspense
      fallback={
        <div className="flex min-h-0 flex-1 items-center justify-center text-muted-foreground text-sm">
          Loading...
        </div>
      }
    >
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
        path: "observables",
        element: (
          <RouteSuspense>
            <Observables />
          </RouteSuspense>
        ),
      },
      {
        path: "observables/:id",
        element: (
          <RouteSuspense>
            <ObservableDetail />
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
      { path: "history/sessions/:id", element: <HistorySessionRedirect /> },
      {
        path: "runtime",
        element: (
          <RouteSuspense>
            <Runtime />
          </RouteSuspense>
        ),
      },
    ],
  },
]);

function HistorySessionRedirect() {
  const { id = "" } = useParams<{ id: string }>();
  return <Navigate to={`/sessions/${encodeURIComponent(id)}`} replace />;
}

export default function App() {
  return <RouterProvider router={router} />;
}
