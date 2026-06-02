// Copyright 2023 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import React, {useCallback, useEffect, useMemo, useRef, useState} from "react";
import {Link, Redirect, Route, Switch, useHistory, useLocation} from "react-router-dom";
import {Helmet} from "react-helmet";
import {Toaster} from "sonner";
import {useTranslation} from "react-i18next";
import i18next from "i18next";
import {Bot, ChevronDown, ChevronLeft, Cloud, Home, LayoutGrid, Lightbulb, Lock, LogIn, LogOut, Menu, MessageSquare, Monitor, Settings, User, Video, Wallet, X} from "lucide-react";
import * as Setting from "./Setting";
import * as AccountBackend from "./backend/AccountBackend";
import * as Conf from "./Conf";
import * as FormBackend from "./backend/FormBackend";
import * as StoreBackend from "./backend/StoreBackend";
import * as FetchFilter from "./backend/FetchFilter";
import {PreviewInterceptor} from "./PreviewInterceptor";
import {cn} from "./lib/utils";

// Page imports
import AuthCallback from "./AuthCallback";
import HomePage from "./HomePage";
import StoreListPage from "./StoreListPage";
import StoreEditPage from "./StoreEditPage";
import FileListPage from "./FileListPage";
import FileViewPage from "./FileViewPage";
import FileTreePage from "./FileTreePage";
import VideoListPage from "./VideoListPage";
import VideoEditPage from "./VideoEditPage";
import VideoPage from "./VideoPage";
import PublicVideoListPage from "./basic/PublicVideoListPage";
import ProviderListPage from "./ProviderListPage";
import ProviderEditPage from "./ProviderEditPage";
import VectorListPage from "./VectorListPage";
import VectorEditPage from "./VectorEditPage";
import SigninPage from "./SigninPage";
import ChatEditPage from "./ChatEditPage";
import ChatListPage from "./ChatListPage";
import MessageListPage from "./MessageListPage";
import MessageEditPage from "./MessageEditPage";
import GraphListPage from "./GraphListPage";
import GraphEditPage from "./GraphEditPage";
import NodeListPage from "./NodeListPage";
import NodeEditPage from "./NodeEditPage";
import MachineListPage from "./MachineListPage";
import MachineEditPage from "./MachineEditPage";
import AssetListPage from "./AssetListPage";
import AssetEditPage from "./AssetEditPage";
import ScanListPage from "./ScanListPage";
import ScanEditPage from "./ScanEditPage";
import ImageListPage from "./ImageListPage";
import ImageEditPage from "./ImageEditPage";
import ContainerListPage from "./ContainerListPage";
import ContainerEditPage from "./ContainerEditPage";
import PodListPage from "./PodListPage";
import PodEditPage from "./PodEditPage";
import SessionListPage from "./SessionListPage";
import ConnectionListPage from "./ConnectionListPage";
import RecordListPage from "./RecordListPage";
import RecordEditPage from "./RecordEditPage";
import WorkflowListPage from "./WorkflowListPage";
import WorkflowEditPage from "./WorkflowEditPage";
import TaskListPage from "./TaskListPage";
import TaskEditPage from "./TaskEditPage";
import FormListPage from "./FormListPage";
import FormEditPage from "./FormEditPage";
import FormDataPage from "./FormDataPage";
import ArticleListPage from "./ArticleListPage";
import ArticleEditPage from "./ArticleEditPage";
import ChatPage from "./ChatPage";
import CustomGithubCorner from "./CustomGithubCorner";
import ShortcutsPage from "./basic/ShortcutsPage";
import UsagePage from "./UsagePage";
import ActivityPage from "./ActivityPage";
import NodeWorkbench from "./NodeWorkbench";
import AccessPage from "./component/access/AccessPage";
import AuditPage from "./frame/AuditPage";
import PythonYolov8miPage from "./frame/PythonYolov8miPage";
import PythonSrPage from "./frame/PythonSrPage";
import SystemInfo from "./SystemInfo";
import OsDesktop from "./OsDesktop";
import TemplateListPage from "./TemplateListPage";
import TemplateEditPage from "./TemplateEditPage";
import ApplicationListPage from "./ApplicationListPage";
import ApplicationEditPage from "./ApplicationEditPage";
import ApplicationStorePage from "./ApplicationStorePage";
import StoreSelect from "./StoreSelect";
import ApplicationDetailsPage from "./ApplicationViewPage";
import HospitalListPage from "./HospitalListPage";
import HospitalEditPage from "./HospitalEditPage";
import DoctorListPage from "./DoctorListPage";
import DoctorEditPage from "./DoctorEditPage";
import PatientListPage from "./PatientListPage";
import PatientEditPage from "./PatientEditPage";
import CaaseListPage from "./CaaseListPage";
import CaaseEditPage from "./CaaseEditPage";
import ConsultationListPage from "./ConsultationListPage";
import ConsultationEditPage from "./ConsultationEditPage";
import AgentsPage from "./AgentsPage";
import VmPage from "./VmPage";
import LanguageSelect from "./LanguageSelect";
import ThemeSelect from "./ThemeSelect";

// Sidebar nav group component
function NavGroup({icon: Icon, label, children, defaultOpen = false}) {
  const [open, setOpen] = useState(defaultOpen);
  const location = useLocation();

  // Auto-open if any child matches current path
  const isActive = useMemo(() => {
    return React.Children.toArray(children).some(child => {
      return child?.props?.to && location.pathname.startsWith(child.props.to);
    });
  }, [children, location.pathname]);

  useEffect(() => {
    if (isActive) {
      setOpen(true);
    }
  }, [isActive]);

  return (
    <div>
      <button
        onClick={() => setOpen(!open)}
        className="w-full flex items-center gap-3 px-3 py-2 text-sm text-zinc-400 hover:text-white hover:bg-zinc-800/50 rounded-md transition-colors"
      >
        {Icon && <Icon className="w-4 h-4 shrink-0" />}
        <span className="flex-1 text-left truncate">{label}</span>
        <ChevronDown className={cn("w-3 h-3 shrink-0 transition-transform", open && "rotate-180")} />
      </button>
      {open && (
        <div className="ml-4 pl-3 border-l border-zinc-800 mt-1 space-y-0.5">
          {children}
        </div>
      )}
    </div>
  );
}

function NavItem({to, children, external}) {
  const location = useLocation();
  const active = location.pathname === to || (to !== "/" && location.pathname.startsWith(to));

  if (external) {
    return (
      <a
        href={to}
        target="_blank"
        rel="noreferrer"
        className="flex items-center gap-2 px-3 py-1.5 text-sm text-zinc-400 hover:text-white hover:bg-zinc-800/50 rounded-md transition-colors"
      >
        {children}
        <svg className="w-3 h-3 ml-auto opacity-50" fill="currentColor" viewBox="0 0 24 24">
          <path d="M21 13v10h-21v-19h12v2h-10v15h17v-8h2zm3-12h-10.988l4.035 4-6.977 7.07 2.828 2.828 6.977-7.07 4.125 4.172v-11z" />
        </svg>
      </a>
    );
  }

  return (
    <Link
      to={to}
      className={cn(
        "flex items-center gap-2 px-3 py-1.5 text-sm rounded-md transition-colors",
        active ? "text-white bg-zinc-800" : "text-zinc-400 hover:text-white hover:bg-zinc-800/50"
      )}
    >
      {children}
    </Link>
  );
}

function App() {
  const history = useHistory();
  const location = useLocation();
  const {t} = useTranslation();

  const [account, setAccount] = useState(undefined);
  const [forms, setForms] = useState([]);
  const [store, setStore] = useState(undefined);
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  const previewInterceptorRef = useRef(null);

  // Initialize config
  useEffect(() => {
    Setting.initServerUrl();
    Setting.initWebConfig();

    const cachedThemeColor = localStorage.getItem("themeColor");
    if (cachedThemeColor) {
      Setting.setThemeColor(cachedThemeColor);
    }

    FetchFilter.initDemoMode();
    Setting.initIamSdk(Conf.AuthConfig);

    if (!Conf.DisablePreviewMode) {
      previewInterceptorRef.current = new PreviewInterceptor(() => account, history);
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // Fetch account
  const getAccount = useCallback(() => {
    AccountBackend.getAccount().then((res) => {
      const acc = res.data;
      if (acc !== null) {
        const language = localStorage.getItem("language");
        if (language !== "" && language !== i18next.language) {
          Setting.setLanguage(language);
        }
      }
      setAccount(acc);
    });
  }, []);

  // Fetch forms
  const getForms = useCallback(() => {
    FormBackend.getForms("admin").then((res) => {
      if (res.status === "ok") {
        setForms(res.data);
      } else {
        Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
      }
    });
  }, []);

  // Fetch store theme
  const getStoreTheme = useCallback(() => {
    StoreBackend.getStore("admin", "_cloud_default_store_").then((res) => {
      if (res.status === "ok" && res.data) {
        const color = res.data.themeColor ? res.data.themeColor : Conf.ThemeDefault.colorPrimary;
        const currentColor = localStorage.getItem("themeColor");
        if (currentColor !== color) {
          Setting.setThemeColor(color);
          localStorage.setItem("themeColor", color);
        }
        setStore(res.data);
      } else {
        Setting.setThemeColor(Conf.ThemeDefault.colorPrimary);
      }
    });
  }, []);

  useEffect(() => {
    getAccount();
    getForms();
    getStoreTheme();
  }, [getAccount, getForms, getStoreTheme]);

  // Close mobile sidebar on navigation
  useEffect(() => {
    setMobileSidebarOpen(false);
  }, [location.pathname]);

  const signout = useCallback(() => {
    AccountBackend.signout().then((res) => {
      if (res.status === "ok") {
        setAccount(null);
        Setting.showMessage("success", i18next.t("account:Successfully signed out, redirected to homepage"));
        Setting.goToLink("/");
      } else {
        Setting.showMessage("error", `${i18next.t("account:Signout failed")}: ${res.msg}`);
      }
    });
  }, []);

  const renderSigninIfNotSignedIn = useCallback((component) => {
    if (account === null) {
      const signinUrl = Setting.getSigninUrl();
      if (signinUrl && signinUrl !== "") {
        sessionStorage.setItem("from", window.location.pathname);
        window.location.replace(signinUrl);
      }
      return null;
    } else if (account === undefined) {
      return null;
    }
    return component;
  }, [account]);

  const renderHomeIfSignedIn = useCallback((component) => {
    if (account !== null && account !== undefined) {
      return <Redirect to="/" />;
    }
    return component;
  }, [account]);

  const isHiddenHeaderAndFooter = useCallback(() => {
    const hiddenPaths = ["/workbench", "/access"];
    return hiddenPaths.some(path => location.pathname.startsWith(path));
  }, [location.pathname]);

  const isWithoutCard = useCallback(() => {
    return Setting.isMobile() || isHiddenHeaderAndFooter() ||
      location.pathname === "/chat" || location.pathname.startsWith("/chat/") || location.pathname === "/";
  }, [isHiddenHeaderAndFooter, location.pathname]);

  const isStoreSelectEnabled = useCallback(() => {
    const uri = location.pathname;
    if (uri.includes("/chat")) {return true;}
    const enabledPrefixes = ["/stores", "/providers", "/vectors", "/chats", "/messages", "/usages", "/files"];
    if (enabledPrefixes.some(prefix => uri.startsWith(prefix))) {return true;}
    if (uri === "/" || uri === "/home") {
      if (Setting.isAnonymousUser(account) || Setting.isChatUser(account) ||
          Setting.isAdminUser(account) || Setting.isChatAdminUser(account) ||
          Setting.getUrlParam("isRaw") !== null) {
        return true;
      }
    }
    return false;
  }, [location.pathname, account]);

  // Build sidebar nav items based on account
  const renderSidebar = () => {
    if (!account) {return null;}

    const isAdmin = Setting.isAdminUser(account);
    const isChatAdmin = Setting.isChatAdminUser(account);

    if (account.type?.startsWith("video-")) {
      return (
        <nav className="space-y-1">
          <NavItem to="/videos">{i18next.t("general:Videos")}</NavItem>
          {account.type === "video-admin-user" && (
            <NavItem to={Setting.getMyProfileUrl(account).replace("/account", "/users")} external>
              {i18next.t("general:Users")}
            </NavItem>
          )}
        </nav>
      );
    }

    if (!isAdmin && (Setting.isAnonymousUser(account) && !Conf.DisablePreviewMode)) {
      if (!isChatAdmin) {
        return (
          <nav className="space-y-1">
            <NavItem to="/">{i18next.t("general:Home")}</NavItem>
          </nav>
        );
      }
    }

    if (isChatAdmin && !isAdmin) {
      return (
        <nav className="space-y-1">
          <NavItem to="/chat">{i18next.t("general:Chat")}</NavItem>
          <NavItem to="/stores">{i18next.t("general:Stores")}</NavItem>
          <NavItem to="/vectors">{i18next.t("general:Vectors")}</NavItem>
          <NavItem to="/chats">{i18next.t("general:Chats")}</NavItem>
          <NavItem to="/messages">{i18next.t("general:Messages")}</NavItem>
          <NavItem to="/usages">{i18next.t("general:Usages")}</NavItem>
          <NavItem to="/activities">{i18next.t("general:Activities")}</NavItem>
          <NavItem to={Setting.getMyProfileUrl(account).replace("/account", "/users")} external>
            {i18next.t("general:Users")}
          </NavItem>
          <NavItem to={Setting.getMyProfileUrl(account).replace("/account", "/resources")} external>
            {i18next.t("general:Resources")}
          </NavItem>
          <NavItem to={Setting.getMyProfileUrl(account).replace("/account", "/permissions")} external>
            {i18next.t("general:Permissions")}
          </NavItem>
        </nav>
      );
    }

    if (Setting.isTaskUser(account)) {
      return (
        <nav className="space-y-1">
          <NavItem to="/tasks">{i18next.t("general:Tasks")}</NavItem>
        </nav>
      );
    }

    // Full admin nav
    return (
      <nav className="space-y-1">
        <NavGroup icon={Home} label={i18next.t("general:Home")} defaultOpen>
          <NavItem to="/chat">{i18next.t("general:Chat")}</NavItem>
          <NavItem to="/usages">{i18next.t("general:Usages")}</NavItem>
          <NavItem to="/activities">{i18next.t("general:Activities")}</NavItem>
          <NavItem to="/desktop">{i18next.t("general:OS Desktop")}</NavItem>
        </NavGroup>

        <NavGroup icon={Lightbulb} label={i18next.t("general:Chats & Messages")}>
          <NavItem to="/chats">{i18next.t("general:Chats")}</NavItem>
          <NavItem to="/messages">{i18next.t("general:Messages")}</NavItem>
        </NavGroup>

        <NavGroup icon={LayoutGrid} label={i18next.t("general:AI Setting")}>
          <NavItem to="/stores">{i18next.t("general:Stores")}</NavItem>
          <NavItem to="/files">{i18next.t("general:Files")}</NavItem>
          <NavItem to="/providers">{i18next.t("general:Providers")}</NavItem>
          <NavItem to="/vectors">{i18next.t("general:Vectors")}</NavItem>
        </NavGroup>

        <NavGroup icon={Cloud} label={i18next.t("general:Cloud Resources")}>
          <NavItem to="/templates">{i18next.t("general:Templates")}</NavItem>
          <NavItem to="/application-store">{i18next.t("general:Application Store")}</NavItem>
          <NavItem to="/applications">{i18next.t("general:Applications")}</NavItem>
          <NavItem to="/nodes">{i18next.t("general:Nodes")}</NavItem>
          <NavItem to="/machines">{i18next.t("general:Machines")}</NavItem>
          <NavItem to="/assets">{i18next.t("general:Assets")}</NavItem>
          <NavItem to="/images">{i18next.t("general:Images")}</NavItem>
          <NavItem to="/containers">{i18next.t("general:Containers")}</NavItem>
          <NavItem to="/pods">{i18next.t("general:Pods")}</NavItem>
          <NavItem to="/workbench" external>{i18next.t("general:Workbench")}</NavItem>
        </NavGroup>

        <NavGroup icon={Video} label={i18next.t("general:Multimedia")}>
          <NavItem to="/videos">{i18next.t("general:Videos")}</NavItem>
          <NavItem to="/public-videos">{i18next.t("general:Public Videos")}</NavItem>
          <NavItem to="/tasks">{i18next.t("general:Tasks")}</NavItem>
          <NavItem to="/forms">{i18next.t("general:Forms")}</NavItem>
          <NavItem to="/workflows">{i18next.t("general:Workflows")}</NavItem>
          <NavItem to="/hospitals">{i18next.t("med:Hospitals")}</NavItem>
          <NavItem to="/doctors">{i18next.t("med:Doctors")}</NavItem>
          <NavItem to="/patients">{i18next.t("med:Patients")}</NavItem>
          <NavItem to="/caases">{i18next.t("med:Caases")}</NavItem>
          <NavItem to="/consultations">{i18next.t("med:Consultations")}</NavItem>
          <NavItem to="/audit">{i18next.t("general:Audit")}</NavItem>
          <NavItem to="/articles">{i18next.t("general:Articles")}</NavItem>
          <NavItem to="/graphs">{i18next.t("general:Graphs")}</NavItem>
          <NavItem to="/scans">{i18next.t("general:Scans")}</NavItem>
        </NavGroup>

        <NavGroup icon={Wallet} label={i18next.t("general:Logging & Auditing")}>
          <NavItem to="/sessions">{i18next.t("general:Sessions")}</NavItem>
          <NavItem to="/connections">{i18next.t("general:Connections")}</NavItem>
          <NavItem to="/records">{i18next.t("general:Records")}</NavItem>
        </NavGroup>

        <NavGroup icon={Lock} label={i18next.t("general:Identity & Access Management")}>
          <NavItem to={Setting.getMyProfileUrl(account).replace("/account", "/users")} external>
            {i18next.t("general:Users")}
          </NavItem>
          <NavItem to={Setting.getMyProfileUrl(account).replace("/account", "/resources")} external>
            {i18next.t("general:Resources")}
          </NavItem>
          <NavItem to={Setting.getMyProfileUrl(account).replace("/account", "/permissions")} external>
            {i18next.t("general:Permissions")}
          </NavItem>
        </NavGroup>

        <NavGroup icon={Bot} label={i18next.t("general:Agents")}>
          <NavItem to="/agents">{i18next.t("general:Dashboard")}</NavItem>
        </NavGroup>

        <NavGroup icon={Monitor} label={i18next.t("general:Virtual Machines")}>
          <NavItem to="/vm">{i18next.t("general:Dashboard")}</NavItem>
        </NavGroup>

        <NavGroup icon={Settings} label={i18next.t("general:Admin")}>
          <NavItem to="/sysinfo">{i18next.t("general:System Info")}</NavItem>
          <NavItem
            to={Setting.isLocalhost() ? `${Setting.ServerUrl}/swagger/index.html` : "/swagger/index.html"}
            external
          >
            {i18next.t("general:Swagger")}
          </NavItem>
        </NavGroup>

        {/* Dynamic form nav items */}
        {forms.slice().sort((a, b) => a.position.localeCompare(b.position)).map(form => (
          <NavItem key={form.name} to={`/forms/${form.name}/data`}>{form.displayName}</NavItem>
        ))}
      </nav>
    );
  };

  const renderAvatar = () => {
    if (!account) {return null;}
    if (account.avatar === "") {
      return (
        <div
          className="w-8 h-8 rounded-full flex items-center justify-center text-xs font-medium text-black"
          style={{backgroundColor: Setting.getAvatarColor(account.name)}}
        >
          {Setting.getShortName(account.name)}
        </div>
      );
    }
    return (
      <img src={account.avatar} alt="" className="w-8 h-8 rounded-full object-cover" />
    );
  };

  const renderUserMenu = () => {
    if (account === undefined) {return null;}

    if (account === null) {
      return (
        <div className="flex items-center gap-2">
          <LanguageSelect />
          <a
            href={Setting.getSigninUrl()}
            className="flex items-center gap-2 px-3 py-1.5 text-sm text-zinc-400 hover:text-white transition-colors"
          >
            <LogIn className="w-4 h-4" />
            {i18next.t("account:Sign In")}
          </a>
        </div>
      );
    }

    return (
      <div className="flex items-center gap-3">
        {Setting.isLocalAdminUser(account) && (
          <StoreSelect
            account={account}
            className="store-select"
            withAll={true}
            style={{display: Setting.isMobile() ? "none" : "flex"}}
            disabled={!isStoreSelectEnabled()}
          />
        )}
        <LanguageSelect />
        <div className="relative group">
          <button className="flex items-center gap-2 px-2 py-1.5 rounded-lg hover:bg-zinc-800 transition-colors">
            {renderAvatar()}
            {!Setting.isMobile() && (
              <span className="text-sm text-zinc-300">{Setting.getShortName(account.displayName)}</span>
            )}
            <ChevronDown className="w-3 h-3 text-zinc-500" />
          </button>
          <div className="absolute right-0 top-full mt-1 w-48 py-1 bg-zinc-900 border border-zinc-800 rounded-lg shadow-xl opacity-0 invisible group-hover:opacity-100 group-hover:visible transition-all z-50">
            {!Setting.isAnonymousUser(account) && (
              <>
                <button
                  onClick={() => Setting.openLink(Setting.getMyProfileUrl(account))}
                  className="w-full flex items-center gap-2 px-3 py-2 text-sm text-zinc-300 hover:text-white hover:bg-zinc-800 transition-colors"
                >
                  <User className="w-4 h-4" />
                  {i18next.t("account:My Account")}
                </button>
                <button
                  onClick={() => history.push("/chat")}
                  className="w-full flex items-center gap-2 px-3 py-2 text-sm text-zinc-300 hover:text-white hover:bg-zinc-800 transition-colors"
                >
                  <MessageSquare className="w-4 h-4" />
                  {i18next.t("general:Chats & Messages")}
                </button>
                <div className="border-t border-zinc-800 my-1" />
                <button
                  onClick={signout}
                  className="w-full flex items-center gap-2 px-3 py-2 text-sm text-zinc-300 hover:text-white hover:bg-zinc-800 transition-colors"
                >
                  <LogOut className="w-4 h-4" />
                  {i18next.t("account:Sign Out")}
                </button>
              </>
            )}
            {Setting.isAnonymousUser(account) && (
              <button
                onClick={() => {
                  history.push(window.location.pathname);
                  Setting.redirectToLogin();
                }}
                className="w-full flex items-center gap-2 px-3 py-2 text-sm text-zinc-300 hover:text-white hover:bg-zinc-800 transition-colors"
              >
                <LogIn className="w-4 h-4" />
                {i18next.t("account:Sign In")}
              </button>
            )}
          </div>
        </div>
      </div>
    );
  };

  const renderRouter = () => {
    if (account?.type?.startsWith("video-")) {
      if (location.pathname === "/") {
        return <PublicVideoListPage account={account} />;
      }
    }

    return (
      <Switch>
        <Route exact path="/access/:owner/:name" render={(props) => renderSigninIfNotSignedIn(<AccessPage account={account} {...props} />)} />
        <Route exact path="/callback" component={AuthCallback} />
        <Route exact path="/signin" render={(props) => renderHomeIfSignedIn(<SigninPage {...props} />)} />
        <Route exact path="/" render={(props) => renderSigninIfNotSignedIn(<HomePage account={account} {...props} />)} />
        <Route exact path="/home" render={(props) => renderSigninIfNotSignedIn(<HomePage account={account} {...props} />)} />
        <Route exact path="/stores" render={(props) => renderSigninIfNotSignedIn(<StoreListPage account={account} {...props} />)} />
        <Route exact path="/stores/:owner/:storeName" render={(props) => renderSigninIfNotSignedIn(<StoreEditPage account={account} {...props} />)} />
        <Route exact path="/stores/:owner/:storeName/view" render={(props) => renderSigninIfNotSignedIn(<FileTreePage account={account} {...props} />)} />
        <Route exact path="/stores/:owner/:storeName/chats" render={(props) => renderSigninIfNotSignedIn(<ChatListPage account={account} {...props} />)} />
        <Route exact path="/stores/:owner/:storeName/messages" render={(props) => renderSigninIfNotSignedIn(<MessageListPage account={account} {...props} />)} />
        <Route exact path="/videos" render={(props) => renderSigninIfNotSignedIn(<VideoListPage account={account} {...props} />)} />
        <Route exact path="/videos/:owner/:videoName" render={(props) => renderSigninIfNotSignedIn(<VideoEditPage account={account} {...props} />)} />
        <Route exact path="/public-videos" render={(props) => <PublicVideoListPage {...props} />} />
        <Route exact path="/public-videos/:owner/:videoName" render={(props) => <VideoPage account={account} {...props} />} />
        <Route exact path="/providers" render={(props) => renderSigninIfNotSignedIn(<ProviderListPage account={account} {...props} />)} />
        <Route exact path="/providers/:providerName" render={(props) => renderSigninIfNotSignedIn(<ProviderEditPage account={account} {...props} />)} />
        <Route exact path="/files" render={(props) => renderSigninIfNotSignedIn(<FileListPage account={account} {...props} />)} />
        <Route exact path="/files/:fileName" render={(props) => renderSigninIfNotSignedIn(<FileViewPage account={account} {...props} />)} />
        <Route exact path="/vectors" render={(props) => renderSigninIfNotSignedIn(<VectorListPage account={account} {...props} />)} />
        <Route exact path="/vectors/:vectorName" render={(props) => renderSigninIfNotSignedIn(<VectorEditPage account={account} {...props} />)} />
        <Route exact path="/chats" render={(props) => renderSigninIfNotSignedIn(<ChatListPage account={account} {...props} />)} />
        <Route exact path="/chats/:chatName" render={(props) => renderSigninIfNotSignedIn(<ChatEditPage account={account} {...props} />)} />
        <Route exact path="/messages" render={(props) => renderSigninIfNotSignedIn(<MessageListPage account={account} {...props} />)} />
        <Route exact path="/messages/:messageName" render={(props) => renderSigninIfNotSignedIn(<MessageEditPage account={account} {...props} />)} />
        <Route exact path="/usages" render={(props) => renderSigninIfNotSignedIn(<UsagePage account={account} {...props} />)} />
        <Route exact path="/activities" render={(props) => renderSigninIfNotSignedIn(<ActivityPage account={account} {...props} />)} />
        <Route exact path="/desktop" render={(props) => <OsDesktop account={account} {...props} />} />
        <Route exact path="/templates" render={(props) => renderSigninIfNotSignedIn(<TemplateListPage account={account} {...props} />)} />
        <Route exact path="/templates/:templateName" render={(props) => renderSigninIfNotSignedIn(<TemplateEditPage account={account} {...props} />)} />
        <Route exact path="/applications" render={(props) => renderSigninIfNotSignedIn(<ApplicationListPage account={account} {...props} />)} />
        <Route exact path="/applications/:applicationName" render={(props) => renderSigninIfNotSignedIn(<ApplicationEditPage account={account} {...props} />)} />
        <Route exact path="/applications/:applicationName/view" render={(props) => renderSigninIfNotSignedIn(<ApplicationDetailsPage account={account} {...props} />)} />
        <Route exact path="/application-store" render={(props) => renderSigninIfNotSignedIn(<ApplicationStorePage account={account} {...props} />)} />
        <Route exact path="/nodes" render={(props) => renderSigninIfNotSignedIn(<NodeListPage account={account} {...props} />)} />
        <Route exact path="/nodes/:nodeName" render={(props) => renderSigninIfNotSignedIn(<NodeEditPage account={account} {...props} />)} />
        <Route exact path="/sessions" render={(props) => renderSigninIfNotSignedIn(<SessionListPage account={account} {...props} />)} />
        <Route exact path="/connections" render={(props) => renderSigninIfNotSignedIn(<ConnectionListPage account={account} {...props} />)} />
        <Route exact path="/records" render={(props) => renderSigninIfNotSignedIn(<RecordListPage account={account} {...props} />)} />
        <Route exact path="/records/:organizationName/:recordName" render={(props) => renderSigninIfNotSignedIn(<RecordEditPage account={account} {...props} />)} />
        <Route exact path="/workbench" render={(props) => renderSigninIfNotSignedIn(<NodeWorkbench account={account} {...props} />)} />
        <Route exact path="/machines" render={(props) => renderSigninIfNotSignedIn(<MachineListPage account={account} {...props} />)} />
        <Route exact path="/machines/:organizationName/:machineName" render={(props) => renderSigninIfNotSignedIn(<MachineEditPage account={account} {...props} />)} />
        <Route exact path="/assets" render={(props) => renderSigninIfNotSignedIn(<AssetListPage account={account} {...props} />)} />
        <Route exact path="/assets/:assetName" render={(props) => renderSigninIfNotSignedIn(<AssetEditPage account={account} {...props} />)} />
        <Route exact path="/scans" render={(props) => renderSigninIfNotSignedIn(<ScanListPage account={account} {...props} />)} />
        <Route exact path="/scans/:scanName" render={(props) => renderSigninIfNotSignedIn(<ScanEditPage account={account} {...props} />)} />
        <Route exact path="/images" render={(props) => renderSigninIfNotSignedIn(<ImageListPage account={account} {...props} />)} />
        <Route exact path="/images/:organizationName/:imageName" render={(props) => renderSigninIfNotSignedIn(<ImageEditPage account={account} {...props} />)} />
        <Route exact path="/containers" render={(props) => renderSigninIfNotSignedIn(<ContainerListPage account={account} {...props} />)} />
        <Route exact path="/containers/:organizationName/:containerName" render={(props) => renderSigninIfNotSignedIn(<ContainerEditPage account={account} {...props} />)} />
        <Route exact path="/pods" render={(props) => renderSigninIfNotSignedIn(<PodListPage account={account} {...props} />)} />
        <Route exact path="/pods/:organizationName/:podName" render={(props) => renderSigninIfNotSignedIn(<PodEditPage account={account} {...props} />)} />
        <Route exact path="/workflows" render={(props) => renderSigninIfNotSignedIn(<WorkflowListPage account={account} {...props} />)} />
        <Route exact path="/workflows/:workflowName" render={(props) => renderSigninIfNotSignedIn(<WorkflowEditPage account={account} {...props} />)} />
        <Route exact path="/audit" render={(props) => renderSigninIfNotSignedIn(<AuditPage account={account} {...props} />)} />
        <Route exact path="/yolov8mi" render={(props) => renderSigninIfNotSignedIn(<PythonYolov8miPage account={account} {...props} />)} />
        <Route exact path="/sr" render={(props) => renderSigninIfNotSignedIn(<PythonSrPage account={account} {...props} />)} />
        <Route exact path="/tasks" render={(props) => renderSigninIfNotSignedIn(<TaskListPage account={account} {...props} />)} />
        <Route exact path="/tasks/:owner/:taskName" render={(props) => renderSigninIfNotSignedIn(<TaskEditPage account={account} {...props} />)} />
        <Route exact path="/forms" render={(props) => renderSigninIfNotSignedIn(<FormListPage account={account} {...props} />)} />
        <Route exact path="/forms/:formName" render={(props) => renderSigninIfNotSignedIn(<FormEditPage account={account} {...props} />)} />
        <Route exact path="/forms/:formName/data" render={(props) => renderSigninIfNotSignedIn(<FormDataPage key={props.match.params.formName} account={account} {...props} />)} />
        <Route exact path="/articles" render={(props) => renderSigninIfNotSignedIn(<ArticleListPage account={account} {...props} />)} />
        <Route exact path="/articles/:articleName" render={(props) => renderSigninIfNotSignedIn(<ArticleEditPage account={account} {...props} />)} />
        <Route exact path="/hospitals" render={(props) => renderSigninIfNotSignedIn(<HospitalListPage account={account} {...props} />)} />
        <Route exact path="/hospitals/:hospitalName" render={(props) => renderSigninIfNotSignedIn(<HospitalEditPage account={account} {...props} />)} />
        <Route exact path="/doctors" render={(props) => renderSigninIfNotSignedIn(<DoctorListPage account={account} {...props} />)} />
        <Route exact path="/doctors/:doctorName" render={(props) => renderSigninIfNotSignedIn(<DoctorEditPage account={account} {...props} />)} />
        <Route exact path="/patients" render={(props) => renderSigninIfNotSignedIn(<PatientListPage account={account} {...props} />)} />
        <Route exact path="/patients/:patientName" render={(props) => renderSigninIfNotSignedIn(<PatientEditPage account={account} {...props} />)} />
        <Route exact path="/caases" render={(props) => renderSigninIfNotSignedIn(<CaaseListPage account={account} {...props} />)} />
        <Route exact path="/caases/:caaseName" render={(props) => renderSigninIfNotSignedIn(<CaaseEditPage account={account} {...props} />)} />
        <Route exact path="/consultations" render={(props) => renderSigninIfNotSignedIn(<ConsultationListPage account={account} {...props} />)} />
        <Route exact path="/consultations/:consultationName" render={(props) => renderSigninIfNotSignedIn(<ConsultationEditPage account={account} {...props} />)} />
        <Route exact path="/chat" render={(props) => renderSigninIfNotSignedIn(<ChatPage account={account} {...props} />)} />
        <Route exact path="/chat/:chatName" render={(props) => renderSigninIfNotSignedIn(<ChatPage account={account} {...props} />)} />
        <Route exact path="/stores/:owner/:storeName/chat" render={(props) => renderSigninIfNotSignedIn(<ChatPage account={account} {...props} />)} />
        <Route exact path="/:owner/:storeName/chat" render={(props) => renderSigninIfNotSignedIn(<ChatPage account={account} {...props} />)} />
        <Route exact path="/:owner/:storeName/chat/:chatName" render={(props) => renderSigninIfNotSignedIn(<ChatPage account={account} {...props} />)} />
        <Route exact path="/graphs" render={(props) => renderSigninIfNotSignedIn(<GraphListPage account={account} {...props} />)} />
        <Route exact path="/graphs/:graphName" render={(props) => renderSigninIfNotSignedIn(<GraphEditPage account={account} {...props} />)} />
        <Route exact path="/agents" render={(props) => renderSigninIfNotSignedIn(<AgentsPage account={account} {...props} />)} />
        <Route exact path="/vm" render={(props) => renderSigninIfNotSignedIn(<VmPage account={account} {...props} />)} />
        <Route exact path="/sysinfo" render={(props) => renderSigninIfNotSignedIn(<SystemInfo account={account} {...props} />)} />
        <Route path="" render={() => (
          <div className="flex flex-col items-center justify-center py-20">
            <h1 className="text-4xl font-bold text-white mb-2">404</h1>
            <p className="text-zinc-400 mb-6">{i18next.t("general:Sorry, the page you visited does not exist.")}</p>
            <a href="/" className="px-4 py-2 bg-white text-black rounded-md text-sm font-medium hover:bg-zinc-200 transition-colors">
              {i18next.t("general:Back Home")}
            </a>
          </div>
        )} />
      </Switch>
    );
  };

  // Raw mode or portal mode
  if (Setting.getUrlParam("isRaw") !== null) {
    return (
      <>
        <Toaster theme="dark" position="top-right" />
        <HomePage account={account} />
      </>
    );
  }

  if (Setting.getSubdomain() === "portal") {
    return (
      <>
        <Toaster theme="dark" position="top-right" />
        <ShortcutsPage account={account} />
      </>
    );
  }

  const showShell = !isHiddenHeaderAndFooter();

  return (
    <>
      <Helmet>
        <title>{Setting.getHtmlTitle(store?.htmlTitle)}</title>
        <link rel="icon" href={Setting.getFaviconUrl(["dark"], store?.faviconUrl)} />
      </Helmet>
      <Toaster theme="dark" position="top-right" />
      <CustomGithubCorner />

      {showShell ? (
        <div className="flex h-screen overflow-hidden bg-background">
          {/* Mobile sidebar overlay */}
          {mobileSidebarOpen && (
            <div
              className="fixed inset-0 bg-black/60 z-40 lg:hidden"
              onClick={() => setMobileSidebarOpen(false)}
            />
          )}

          {/* Sidebar */}
          <aside className={cn(
            "fixed lg:static inset-y-0 left-0 z-50 flex flex-col bg-zinc-950 border-r border-zinc-800 transition-all duration-200",
            sidebarOpen ? "w-64" : "w-0 lg:w-16",
            mobileSidebarOpen ? "translate-x-0" : "-translate-x-full lg:translate-x-0"
          )}>
            {/* Sidebar header */}
            <div className="flex items-center h-14 px-4 border-b border-zinc-800 shrink-0">
              <Link to="/" className="flex items-center gap-2 min-w-0">
                <img
                  src={Setting.getLogo(["dark"], store?.logoUrl)}
                  alt="Logo"
                  className={cn("h-7 object-contain", !sidebarOpen && "lg:hidden")}
                />
              </Link>
              <button
                onClick={() => setSidebarOpen(!sidebarOpen)}
                className="ml-auto p-1 text-zinc-500 hover:text-white rounded transition-colors hidden lg:block"
              >
                <ChevronLeft className={cn("w-4 h-4 transition-transform", !sidebarOpen && "rotate-180")} />
              </button>
              <button
                onClick={() => setMobileSidebarOpen(false)}
                className="ml-auto p-1 text-zinc-500 hover:text-white rounded transition-colors lg:hidden"
              >
                <X className="w-4 h-4" />
              </button>
            </div>

            {/* Sidebar nav */}
            <div className={cn("flex-1 overflow-y-auto px-3 py-4", !sidebarOpen && "lg:hidden")}>
              {renderSidebar()}
            </div>

            {/* Sidebar footer */}
            <div className={cn("border-t border-zinc-800 p-3", !sidebarOpen && "lg:hidden")}>
              <div className="text-xs text-zinc-600" dangerouslySetInnerHTML={{__html: Setting.getFooterHtml(["dark"], store?.footerHtml)}} />
            </div>
          </aside>

          {/* Main content */}
          <div className="flex-1 flex flex-col min-w-0">
            {/* Top bar */}
            <header className="flex items-center h-14 px-4 border-b border-zinc-800 bg-zinc-950 shrink-0">
              <button
                onClick={() => {
                  if (Setting.isMobile()) {
                    setMobileSidebarOpen(true);
                  } else {
                    setSidebarOpen(!sidebarOpen);
                  }
                }}
                className="p-1.5 text-zinc-400 hover:text-white rounded-md hover:bg-zinc-800 transition-colors mr-3"
              >
                <Menu className="w-5 h-5" />
              </button>
              <div className="flex-1" />
              {renderUserMenu()}
            </header>

            {/* Page content */}
            <main className="flex-1 overflow-y-auto">
              {isWithoutCard() ? (
                renderRouter()
              ) : (
                <div className="p-6">
                  <div className="content-warp-card">
                    {renderRouter()}
                  </div>
                </div>
              )}
            </main>
          </div>
        </div>
      ) : (
        <div className="min-h-screen">
          {renderRouter()}
        </div>
      )}
    </>
  );
}

export default App;
