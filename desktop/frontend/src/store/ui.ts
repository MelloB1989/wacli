import {create} from 'zustand'

export type AppTab = 'dashboard' | 'chats' | 'logs' | 'automation' | 'assistant'

type UIState = {
  activeTab: AppTab
  chatFilter: string
  chatQuery: string
  loginPairCode: boolean
  loginPhone: string
  setActiveTab: (tab: AppTab) => void
  setChatFilter: (filter: string) => void
  setChatQuery: (query: string) => void
  setLoginPairCode: (pairCode: boolean) => void
  setLoginPhone: (phone: string) => void
}

export const useUIStore = create<UIState>((set) => ({
  activeTab: 'dashboard',
  chatFilter: 'all',
  chatQuery: '',
  loginPairCode: false,
  loginPhone: '',
  setActiveTab: (activeTab) => set({activeTab}),
  setChatFilter: (chatFilter) => set({chatFilter}),
  setChatQuery: (chatQuery) => set({chatQuery}),
  setLoginPairCode: (loginPairCode) => set({loginPairCode}),
  setLoginPhone: (loginPhone) => set({loginPhone}),
}))
