import { CustomLibraryBanner } from "@/app/(main)/_features/anime-library/_containers/custom-library-banner"
import { useIsAdmin } from "@/app/(main)/_hooks/use-server-status"
import { AutoDownloaderPage } from "@/app/(main)/auto-downloader/_containers/autodownloader-page"
import { LuffyError } from "@/components/shared/luffy-error"
import { PageWrapper } from "@/components/shared/page-wrapper"
import React from "react"


export default function Page() {

    const isAdmin = useIsAdmin()

    return (
        <>
            <CustomLibraryBanner discrete />
            <PageWrapper className="p-4 sm:p-8 space-y-4">
                <div className="flex justify-between items-center w-full relative">
                    <div>
                        <h2>Auto Downloader</h2>
                        <p className="text-[--muted]">
                            Automatically download new episodes as they are released.
                        </p>
                    </div>
                </div>
                {isAdmin ? <AutoDownloaderPage /> : (
                    <LuffyError title="Admin only">
                        Only the server admin can manage the Auto Downloader.
                    </LuffyError>
                )}
            </PageWrapper>
        </>
    )

}
