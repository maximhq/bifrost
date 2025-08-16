import React from 'react'
import { Card, CardContent, CardHeader } from './card'

interface PromoCardItem {
  title: string | React.ReactElement
  description: string | React.ReactElement
}

interface PromoCardStackProps {
  cards: PromoCardItem[]
  className?: string
}

export function PromoCardStack({ cards, className = '' }: PromoCardStackProps) {
  if (!cards || cards.length === 0) {
    return null
  }

  return (
    <div className={`flex flex-col gap-2 ${className}`}>
      {cards.map((card, index) => (
        <Card key={index} className="w-full py-2 px-2.5 shadow-none gap-2 rounded-lg">
          <CardHeader className="text-muted-foreground p-1 text-xs font-medium">
            {typeof card.title === 'string' ? card.title : card.title}
          </CardHeader>
          <CardContent className="text-muted-foreground px-1 pt-0 pb-1 text-xs mt-0">
            {typeof card.description === 'string' ? card.description : card.description}
          </CardContent>
        </Card>
      ))}
    </div>
  )
}
